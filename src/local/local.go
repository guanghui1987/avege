package local

import (
	"flag"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"time"

	"common"
	"common/cache"
	"common/domain"
	iputil "common/ip"
	"github.com/DeanThompson/ginpprof"
	"github.com/gin-gonic/gin"
	"inbound/redir"
	"inbound/socks"
)

const (
	broadcastAddr = "224.225.236.237:51290"
)

var (
	quit = make(chan bool)
)

func run() {
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{
		IP:   net.ParseIP(config.InBoundConfig.Address),
		Port: config.InBoundConfig.Port,
		Zone: "",
	})
	if err != nil {
		common.Panic("Failed listening", err)
		return
	}

	var inboundHandler func(*net.TCPConn, common.OutboundHandler)
	switch config.InBoundConfig.Type {
	case "socks5", "socks":
		common.Infof("starting socks server at %s:%d ...\n", config.InBoundConfig.Address, config.InBoundConfig.Port)
		inboundHandler = socks.HandleInbound
	case "redir":
		common.Infof("starting redir mode at %s:%d ...\n", config.InBoundConfig.Address, config.InBoundConfig.Port)
		inboundHandler = redir.HandleInbound
	}

	for {
		conn, err := ln.AcceptTCP()
		if err != nil {
			common.Error("accept err: ", err)
			continue
		}
		if leftQuote <= 0 {
			common.Fatal("no quote now, please charge in time")
			conn.Close()
			continue
		}
		go inboundHandler(conn, handleOutbound)
	}
}

func dialUDP() (conn *net.UDPConn, err error) {
	var addr *net.UDPAddr
	addr, err = net.ResolveUDPAddr("udp", broadcastAddr)
	if err != nil {
		common.Error("Can't resolve broadcast address", err)
		return
	}

	conn, err = net.DialUDP("udp", nil, addr)
	if err != nil {
		common.Error("Dialing broadcast failed", err)
	}
	return
}

func timers() {
	var conn *net.UDPConn
	var err error
	if config.Generals.BroadcastEnabled {
		for ; ; time.Sleep(3 * time.Second) {
			if conn, err = dialUDP(); err == nil {
				break
			}
		}
	}

	secondTicker := time.NewTicker(1 * time.Second)
	fifteenSecondsTicker := time.NewTicker(15 * time.Second)
	minuteTicker := time.NewTicker(1 * time.Minute)
	hourTicker := time.NewTicker(1 * time.Hour)
	dayTicker := time.NewTicker(24 * time.Hour)
	weekTicker := time.NewTicker(7 * 24 * time.Hour)
	for {
		select {
		case <-secondTicker.C:
			go Statistics.UpdateBps()
			if config.Generals.BroadcastEnabled {
				if conn == nil {
					common.Warning("broadcast UDP conn is nil")
					conn, _ = dialUDP()
					if conn == nil {
						common.Warning("recreating UDP conn failed")
						break
					}
				}
				if _, err = conn.Write([]byte(config.Generals.Token)); err != nil {
					common.Error("failed to broadcast", err)
					conn.Close()
					conn, _ = dialUDP()
				}
			}
		case <-fifteenSecondsTicker.C:
			go Statistics.UpdateLatency()
		case <-minuteTicker.C:
			go uploadStatistic()
		case <-hourTicker.C:
			go Statistics.UpdateServerIP()
		case <-dayTicker.C:
			go updateRules()
		case <-weekTicker.C:
			go iputil.LoadChinaIPList(true)
			go domain.UpdateDomainNameInChina()
			go domain.UpdateDomainNameToBlock()
			go domain.UpdateGFWList()
		case <-quit:
			goto doQuit
		}
	}

doQuit:
	secondTicker.Stop()
	minuteTicker.Stop()
	hourTicker.Stop()
	dayTicker.Stop()
	weekTicker.Stop()

	if config.Generals.BroadcastEnabled {
		conn.Close()
	}
}

// Main the entry of this program
func Main() {
	var configFile string
	var printVer bool

	flag.BoolVar(&printVer, "version", false, "print (v)ersion")
	flag.StringVar(&configFile, "c", "config.json", "(s)pecify config file")

	flag.Parse()

	if printVer {
		common.PrintVersion()
		os.Exit(0)
	}

	// read config file
	var err error
	configFile, err = common.GetConfigPath(configFile)
	if err != nil {
		common.Panic("config file not found")
	}

	if err = parseMultiServersConfigFile(configFile); err != nil {
		common.Panic("parsing multi servers config file failed: ", err)
	}

	configFileChanged := make(chan bool)
	go func() {
		for {
			select {
			case <-configFileChanged:
				if err = parseMultiServersConfigFile(configFile); err != nil {
					common.Error("reloading multi servers config file failed: ", err)
				} else {
					common.Debug("reload", configFile)
				}
			}
		}
	}()
	go common.MonitorFileChanegs(configFile, configFileChanged)
	// end reading config file

	common.DebugLevel = common.DebugLog(config.Generals.LogLevel)

	if config.Generals.APIEnabled {
		if config.Generals.GenRelease {
			gin.SetMode(gin.ReleaseMode)
		}
		r := gin.Default()
		r.LoadHTMLGlob("templates/*")
		v1 := r.Group("/v1")
		{
			v1.GET("/ping", pong) // test purpose
			v1.GET("/statistics.xml", statisticsXMLHandler)
			v1.GET("/statistics.json", statisticsJSONHandler)
			v1.GET("/startSSHReverse", startSSHReverseHandler)
			v1.GET("/stopSSHReverse", stopSSHReverseHandler)
			v1.POST("/startSSHReverse", startSSHReverseHandler)
			v1.POST("/stopSSHReverse", stopSSHReverseHandler)
			v1.POST("/server/add", addServerHandler)
			v1.POST("/server/add/full", addServerFullHandler)
			v1.POST("/server/delete", removeServerHandler)
			v1.POST("/server/select", postSelectServerHandler)
			v1.POST("/server/lastused/update", forceUpdateSmartUsedServerInfoHandler)
			v1.POST("/method", setMethodHandler)
			v1.GET("/method", getMethodHandler)
			v1.POST("/port", setPortHandler)
			v1.GET("/port", getPortHandler)
			v1.POST("/key", setKeyHandler)
			v1.GET("/key", getKeyHandler)
			v1.GET("/token", getTokenHandler)
			v1.POST("/rules/iptables/update", updateIptablesRulesHandler)
			v1.POST("/dns/clear", clearDNSCache)
		}
		h5 := r.Group("/h5")
		{
			h5.GET("/", statisticsHTMLHandler)
			h5.GET("/server/select/:id", getSelectServerHandler)
		}

		r.Static("/assets", "./resources/assets")
		r.StaticFS("/static", http.Dir("./resources/static"))
		r.StaticFile("/favicon.ico", "./resources/favicon.ico")

		if config.Generals.PProfEnabled {
			ginpprof.Wrapper(r)
		}
		go r.Run(config.Generals.API) // listen and serve
	}

	ApplyGeneralConfig()

	switch config.Generals.CacheService {
	case "redis":
		cache.DefaultRedisKey = "avegeClient"
	}
	cache.Init(config.Generals.CacheService)

	startDNSProxy()
	Statistics.LoadFromCache()
	go consoleWS()
	go updateRules()
	go getQuote()
	go Statistics.UpdateLatency()
	go Statistics.UpdateServerIP()
	go timers()

	run()
}
