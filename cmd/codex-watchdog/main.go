package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/awangs1986/gptissobad/internal/control"
	"github.com/awangs1986/gptissobad/internal/openpage"
	"github.com/awangs1986/gptissobad/internal/tray"
)

func main() {
	log.SetFlags(0)
	noTray := flag.Bool("no-tray", false, "only serve the Control Page")
	writeIcon := flag.String("write-icon", "", "write the panel icon PNG and exit")
	flag.Parse()
	if *writeIcon != "" {
		if err := tray.WritePNG(*writeIcon); err != nil {
			log.Fatal(err)
		}
		return
	}

	rt := control.New(control.DefaultPaths())
	addr := net.JoinHostPort("127.0.0.1", rt.State().UIPort)
	page := openpage.URL(rt.State().UIPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("already running, opening %s", page)
		if openErr := openpage.Open(page); openErr != nil {
			log.Fatal(openErr)
		}
		return
	}

	if err := rt.StartIfEnabled(); err != nil {
		rt.Log("auto-start failed: " + err.Error())
	}
	rt.Log("Control Page at " + page)

	srv := &http.Server{
		Handler:           control.Handler(rt, control.FrontendFS()),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		<-ch
		rt.Stop()
		_ = srv.Close()
		os.Exit(0)
	}()

	log.Printf("Control Page %s", page)
	if *noTray {
		select {}
	}
	if !tray.Available() {
		log.Printf("panel icon not available on %s; the Control Page stays up at %s", runtime.GOOS, page)
		select {}
	}
	if err := tray.Run(func() {
		_ = openpage.Open(page)
	}, func() (bool, bool, string) {
		st := rt.State()
		ok := st.Enabled && st.Running && st.HasKey && (!st.FrontEnabled || st.FrontReachable)
		if !ok {
			return false, false, "已停止或故障，点按打开 Control Page"
		}
		if st.Translating {
			return true, true, "正在翻译…，点按打开 Control Page"
		}
		return true, false, "翻译正常，点按打开 Control Page"
	}, func(msg string) { log.Print(msg) }); err != nil {
		log.Printf("panel icon unavailable: %v", err)
		log.Printf("open %s from the browser, or pin the AppImage to the Mint panel", page)
		select {}
	}
}
