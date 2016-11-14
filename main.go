package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"

	"github.com/eBay/fabio/admin"
	"github.com/eBay/fabio/config"
	"github.com/eBay/fabio/exit"
	"github.com/eBay/fabio/metrics"
	"github.com/eBay/fabio/proxy"
	"github.com/eBay/fabio/registry"
	"github.com/eBay/fabio/registry/consul"
	"github.com/eBay/fabio/registry/file"
	"github.com/eBay/fabio/registry/static"
	"github.com/eBay/fabio/route"
)

// version contains the version number
//
// It is set by build/release.sh for tagged releases
// so that 'go get' just works.
//
// It is also set by the linker when fabio
// is built via the Makefile or the build/docker.sh
// script to ensure the correct version nubmer
var version = "1.3.4"

func main() {
	// 从配置文件中获取全局配置，并赋值给配置变量
	cfg, err := config.Load()
	if err != nil {
		exit.Fatalf("[FATAL] %s. %s", version, err)
	}
	if cfg == nil {
		fmt.Println(version)
		return
	}

	// 打印启动信息
	log.Printf("[INFO] Runtime config\n" + toJSON(cfg))
	log.Printf("[INFO] Version %s starting", version)
	log.Printf("[INFO] Go runtime is %s", runtime.Version())

	// 加上程序退出监听goroutine
	exit.Listen(func(s os.Signal) {
		if registry.Default == nil {
			return
		}
		// 从fabio移除服务注册信息
		registry.Default.Deregister()
	})

	// 创建HTTP代理的句柄
	httpProxy := newHTTPProxy(cfg)
	// @todo 了解业务流程
	// SNI 即 Server Name Indication 是用来改善
	// SSL(Secure Socket Layer)和TLS(Transport Layer Security)的一项特性。
	// 它允许客户端在服务器端向其发送证书之前请求服务器的域名。这对于在虚拟主机模式使用TLS是必要的。
	//
	// 即提供 HTTPS 服务, 返回 tcpSNIProxy 结构体
	tcpProxy := proxy.NewTCPSNIProxy(cfg.Proxy)

	// 初始化运行时
	initRuntime(cfg)
	// 设置Metrics监控系统的配置信息，以及路由的服务注册信息
	initMetrics(cfg)
	// 初始化注册服务的后端配置信息
	initBackend(cfg)
	// 监听后端服务器 @todo 了解业务流程
	go watchBackend()
	// 启动管理界面 @todo 了解业务流程
	startAdmin(cfg)
	// 启动监听，开启服务器 @todo 了解业务流程
	startListeners(cfg.Listen, cfg.Proxy.ShutdownWait, httpProxy, tcpProxy)

	//等待退出
	exit.Wait()
}

/**
  使用配置信息创建并返回HTTP代理服务器的句柄
 */
func newHTTPProxy(cfg *config.Config) http.Handler {
	// 设置路由拣选策略
	if err := route.SetPickerStrategy(cfg.Proxy.Strategy); err != nil {
		exit.Fatal("[FATAL] ", err)
	}
	log.Printf("[INFO] Using routing strategy %q", cfg.Proxy.Strategy)

	// 设置路由匹配器
	if err := route.SetMatcher(cfg.Proxy.Matcher); err != nil {
		exit.Fatal("[FATAL] ", err)
	}
	log.Printf("[INFO] Using routing matching %q", cfg.Proxy.Matcher)

	// 配置转换器
	tr := &http.Transport{
		ResponseHeaderTimeout: cfg.Proxy.ResponseHeaderTimeout,
		MaxIdleConnsPerHost:   cfg.Proxy.MaxConn,
		Dial: (&net.Dialer{
			Timeout:   cfg.Proxy.DialTimeout,
			KeepAlive: cfg.Proxy.KeepAliveTimeout,
		}).Dial,
	}
	/**
	@todo 上面代码中有疑问，如下代码：

	Dial: (&net.Dialer{
		Timeout:   cfg.Proxy.DialTimeout,
		KeepAlive: cfg.Proxy.KeepAliveTimeout,
	}).Dial

	第一行为何用 &net.Dialer ? 即为何使用引用？
	原因是 net包的Dialer结构体(struct)的方法Dial是指针类型，所以只有使用引用定义的时候才能访问到该函数
	 */

	// 生成并返回HTTP代理句柄
	return proxy.NewHTTPProxy(tr, cfg.Proxy)
}

/**
 启动管理UI服务,使用配置文件中的 UI配置信息
 "UI": {
        "Addr": ":9998",
        "Color": "light-green",
        "Title": ""
    },
 */
func startAdmin(cfg *config.Config) {
	log.Printf("[INFO] Admin server listening on %q", cfg.UI.Addr)
	go func() {
		if err := admin.ListenAndServe(cfg, version); err != nil {
			exit.Fatal("[FATAL] ui: ", err)
		}
	}()
}

/**
 @todo Metrics 用来做什么？
 系统监控
 使用 配置文件中的 Metrics 信息来设置，Metrics的默认注册表和路由器的服务注册表
 */
func initMetrics(cfg *config.Config) {
	// 如果度量服务器的Target 为空，那么表示Metrics功能被禁用
	if cfg.Metrics.Target == "" {
		log.Printf("[INFO] Metrics disabled")
		return
	}

	var err error
	if metrics.DefaultRegistry, err = metrics.NewRegistry(cfg.Metrics); err != nil {
		exit.Fatal("[FATAL] ", err)
	}
	if route.ServiceRegistry, err = metrics.NewRegistry(cfg.Metrics); err != nil {
		exit.Fatal("[FATAL] ", err)
	}
}

/**
  配置运行时信息
 */
func initRuntime(cfg *config.Config) {

	// GC 百分比，当内存占用达到总内存的百分比后触发GC
	if os.Getenv("GOGC") == "" {
		log.Print("[INFO] Setting GOGC=", cfg.Runtime.GOGC)
		debug.SetGCPercent(cfg.Runtime.GOGC)
	} else {
		log.Print("[INFO] Using GOGC=", os.Getenv("GOGC"), " from env")
	}

	// 最大CPU使用数
	if os.Getenv("GOMAXPROCS") == "" {
		log.Print("[INFO] Setting GOMAXPROCS=", cfg.Runtime.GOMAXPROCS)
		runtime.GOMAXPROCS(cfg.Runtime.GOMAXPROCS)
	} else {
		log.Print("[INFO] Using GOMAXPROCS=", os.Getenv("GOMAXPROCS"), " from env")
	}
}

// 初始化后端服务器的配置信息
// 初始后端注册服务的默认 registry.Default 注册服务及配置信息
func initBackend(cfg *config.Config) {
	var err error

	// 根据配置中的　Registry -> Backend 的数据(file | static | consul)来判断后端服务的类型，并生成相应的配置信息
	switch cfg.Registry.Backend {
	case "file":
		registry.Default, err = file.NewBackend(cfg.Registry.File.Path)
	case "static":
		registry.Default, err = static.NewBackend(cfg.Registry.Static.Routes)
	case "consul":
		registry.Default, err = consul.NewBackend(&cfg.Registry.Consul)
	default:
		exit.Fatal("[FATAL] Unknown registry backend ", cfg.Registry.Backend)
	}

	if err != nil {
		exit.Fatal("[FATAL] Error initializing backend. ", err)
	}
	if err := registry.Default.Register(); err != nil {
		exit.Fatal("[FATAL] Error registering backend. ", err)
	}
}

/**
  启动监测服务器的后端服务
 */
func watchBackend() {
	var (
		last   string
		svccfg string
		mancfg string
	)

	svc := registry.Default.WatchServices()
	man := registry.Default.WatchManual()

	for {
		select {
		case svccfg = <-svc:
		case mancfg = <-man:
		}

		// manual config overrides service config
		// order matters
		next := svccfg + "\n" + mancfg
		if next == last {
			continue
		}

		t, err := route.ParseString(next)
		if err != nil {
			log.Printf("[WARN] %s", err)
			continue
		}
		route.SetTable(t)

		last = next
	}
}

func toJSON(v interface{}) string {
	data, err := json.MarshalIndent(v, "", "    ")
	if err != nil {
		panic("json: " + err.Error())
	}
	return string(data)
}
