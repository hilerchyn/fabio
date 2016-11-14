package main

import (
	"crypto/tls"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/armon/go-proxyproto"
	"github.com/eBay/fabio/cert"
	"github.com/eBay/fabio/config"
	"github.com/eBay/fabio/exit"
	"github.com/eBay/fabio/proxy"
)

var quit = make(chan bool)

func init() {
	exit.Listen(func(os.Signal) { close(quit) })
}

// startListeners runs one or more listeners for the handler
/*
  listen = "Listen": [
        {
            "Addr": ":9999",
            "Proto": "http",
            "ReadTimeout": 0,
            "WriteTimeout": 0,
            "CertSource": {
                "Name": "",
                "Type": "",
                "CertPath": "",
                "KeyPath": "",
                "ClientCAPath": "",
                "CAUpgradeCN": "",
                "Refresh": 0,
                "Header": null
            },
            "StrictMatch": false
        }
    ],
 通过配置信息中的 Listen 来启动不同的监听服务，根据 上面的 Proto 来启动不懂的服务器， Proto 可用的参数有 http, https, tcp+sni
 */
func startListeners(listen []config.Listen, wait time.Duration, h http.Handler, tcph proxy.TCPProxy) {
	for _, l := range listen {
		switch l.Proto {
		case "tcp+sni":
			go listenAndServeTCP(l, tcph)
		case "http", "https":
			go listenAndServeHTTP(l, h)
		default:
			panic("invalid protocol: " + l.Proto)
		}
	}

	// wait for shutdown signal
	<-quit

	// disable routing for all requests
	proxy.Shutdown()

	// trigger graceful shutdown
	log.Printf("[INFO] Graceful shutdown over %s", wait)
	time.Sleep(wait)
	log.Print("[INFO] Down")
}

/*
 使用了第三方 github.com/armon/go-proxyproto 包
 @todo 两次调用了 ln.Close() 为什么不会引发问题？
 	defer ln.Close()

	// close the socket on exit to terminate the accept loop
	go func() {
		<-quit
		ln.Close()
	}()
 */
func listenAndServeTCP(l config.Listen, h proxy.TCPProxy) {
	log.Print("[INFO] TCP+SNI proxy listening on ", l.Addr)

	// 生成 Listener 结构体类型
	ln, err := net.Listen("tcp", l.Addr)
	if err != nil {
		exit.Fatal("[FATAL] ", err)
	}
	ln = &proxyproto.Listener{Listener: tcpKeepAliveListener{ln.(*net.TCPListener)}}
	defer ln.Close()

	// close the socket on exit to terminate the accept loop
	go func() {
		<-quit
		ln.Close()
	}()

	for {
		// 接收连接请求
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-quit:
				return
			default:
				exit.Fatal("[FATAL] ", err)
			}
		}
		// 处理连接
		go h.Serve(conn)
	}
}

func listenAndServeHTTP(l config.Listen, h http.Handler) {
	srv := &http.Server{
		Handler:      h,
		Addr:         l.Addr,
		ReadTimeout:  l.ReadTimeout,
		WriteTimeout: l.WriteTimeout,
	}

	if l.Proto == "https" {
		src, err := cert.NewSource(l.CertSource)
		if err != nil {
			exit.Fatal("[FATAL] ", err)
		}

		srv.TLSConfig, err = cert.TLSConfig(src, l.StrictMatch)
		if err != nil {
			exit.Fatal("[FATAL] ", err)
		}
	}

	if srv.TLSConfig != nil {
		log.Printf("[INFO] HTTPS proxy listening on %s", l.Addr)
		if srv.TLSConfig.ClientAuth == tls.RequireAndVerifyClientCert {
			log.Printf("[INFO] Client certificate authentication enabled on %s", l.Addr)
		}
	} else {
		log.Printf("[INFO] HTTP proxy listening on %s", l.Addr)
	}

	if err := serve(srv); err != nil {
		exit.Fatal("[FATAL] ", err)
	}
}

func serve(srv *http.Server) error {
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		exit.Fatal("[FATAL] ", err)
	}

	ln = &proxyproto.Listener{Listener: tcpKeepAliveListener{ln.(*net.TCPListener)}}

	if srv.TLSConfig != nil {
		ln = tls.NewListener(ln, srv.TLSConfig)
	}

	return srv.Serve(ln)
}

// copied from http://golang.org/src/net/http/server.go?s=54604:54695#L1967
// tcpKeepAliveListener sets TCP keep-alive timeouts on accepted
// connections. It's used by ListenAndServe and ListenAndServeTLS so
// dead TCP connections (e.g. closing laptop mid-download) eventually
// go away.
type tcpKeepAliveListener struct {
	*net.TCPListener
}

func (ln tcpKeepAliveListener) Accept() (c net.Conn, err error) {
	tc, err := ln.AcceptTCP()
	if err != nil {
		return
	}
	if err = tc.SetKeepAlive(true); err != nil {
		return
	}
	if err = tc.SetKeepAlivePeriod(3 * time.Minute); err != nil {
		return
	}
	return tc, nil
}
