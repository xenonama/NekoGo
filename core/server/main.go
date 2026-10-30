//go:build !darwin

package main

import (
	"flag"
	"fmt"
	"log"
	"nekobox_core/gen"
	main_sing "nekobox_core/gen/main_sing"
	"nekobox_core/internal"
	"nekobox_core/internal/boxmain"
	_ "nekobox_core/internal/distro/all"
	"net"
	"os"
	"os/signal"
	"runtime"
	runtimeDebug "runtime/debug"
	"strconv"
	"syscall"
	"time"

	"github.com/apache/thrift/lib/go/thrift"
	C "github.com/sagernet/sing-box/constant"
)

func RunCore(addr net.Addr, _debug *bool) {
	if err := wsainit(); err != nil {
		log.Printf("WSA init failed: %v", err)
		return
	}

	osSignals := make(chan os.Signal, 1)
	signal.Notify(osSignals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer func() {
		signal.Stop(osSignals)
		close(osSignals)
	}()

	go func() {
		_, loaded := <-osSignals
		if !loaded {
			return
		}
		shutdownInstance()
		os.Exit(0)
	}()

	internal.Debug = *_debug

	log.Printf("Ruleset dir is %s", internal.GetRulesetCachedir())

	boxmain.DisableColor()
	// RPC readiness probe (BUG-034): bounded retries so a process
	// whose bind failed exits loudly instead of dial-spinning forever.
	// A successful dial only proves SOMETHING listens on the address
	// (a second process on the same port would false-report via the
	// first owner's listener), so the authoritative signal remains the
	// Serve error below, which exits loudly on a bind failure.
	go func() {
		if err := wsainit(); err != nil {
			log.Printf("WSA init failed: %v", err)
			return
		}
		network := addr.Network()
		address := addr.String()
		const maxProbeRetries = 50 // 50 x 100ms = 5s
		for i := 0; i < maxProbeRetries; i++ {
			time.Sleep(100 * time.Millisecond)
			conn, err := net.Dial(network, address)
			if err == nil {
				conn.Close()
				fmt.Printf("Core listening at %v\n", address)
				if network == "unix" {
					os.Chmod(address, 0770)
				}
				return
			}
		}
		log.Printf("Core failed to listen on %v after 5s; exiting", address)
		os.Exit(1)
	}()
	{
		transportFactory := thrift.NewTBufferedTransportFactory(8192)
		config := &thrift.TConfiguration{
			ConnectTimeout: time.Second * 3,  // 3 second connection timeout (matches gui client)
			SocketTimeout:  time.Second * 30, // 30 second socket read/write timeout (covers SpeedTest)
			MaxMessageSize: 1024 * 1024 * 50, // 50 MB maximum message size
		}

		// 2. Create the TBinaryProtocolFactory using the configuration
		protocolFactory := thrift.NewTBinaryProtocolFactoryConf(config)
		//	addr, err := net.ResolveTCPAddr("tcp", listenAddr)
		//	if err != nil {
		//		log.Println("error running thrift server: ", err)
		//	}
		if err := wsainit(); err != nil {
			log.Printf("WSA init failed: %v", err)
			return
		}

		transport := thrift.NewTServerSocketFromAddrTimeout(addr, 0)
		handler := &server{}
		processor := gen.NewLibcoreServiceProcessor(handler)
		server := thrift.NewTSimpleServer4(processor, transport, transportFactory, protocolFactory)
		if err := server.Serve(); err != nil {
			log.Fatalf("thrift serve: %v", err)
		}
	}
}

func main() {
	skip_environment_lookup := false
	{
		len_os_args := len(os.Args)
		if len_os_args > 1 {
			first_arg := os.Args[1]
			switch first_arg {
			case "sing-box":
				os.Args = os.Args[1:]
				main_sing.MainFunc()
				return
			case "core":
				skip_environment_lookup = true
			}
			switch runtime.GOOS {
			case "windows":
				switch first_arg {
				case "launch-server-mode":
					if len_os_args > 2 {
						fmt.Printf("nekobox_core launcher mode")
						doRun(os.Args[2])
						return
					}
				case "-installer-mode":
					fmt.Println("nekobox_core installer mode")
					InstallerMode()
					return
				}
			}
		}
	}
	if !skip_environment_lookup {
		v, ok := os.LookupEnv("InTheNameOf")
		if ok {
			ok = (v == "Iblis")
		}
		if !ok {
			main_sing.MainFunc()
			return
		}
	} else {
		var args []string
		args = append(args, os.Args[0])
		args = append(args, os.Args[2:]...)
		os.Args = args
	}

	var _admin *bool
	var _save *bool
	var _waitpid *int

	_port := flag.Int("port", 19810, "Port")
	_addr := flag.String("address", "127.0.0.1", "Address")
	_sock := flag.String("socket", "", "Unix Domain Socket")
	_debug := flag.Bool("debug", false, "Debug mode")
	_arg0 := flag.String("argv0", os.Args[0], "Replace first argument")
	_ruleset_cachedir := flag.String("ruleset-cache-directory", "", "Set ruleset cache directory")

	var comment string
	if runtime.GOOS == "windows" {
		comment = "Register windows elevated task for executable"
	} else {
		CheckResolvectl()
		comment = "Set admin capabilities to executable"
	}
	_save = flag.Bool("save", false, comment)

	_admin = flag.Bool("admin", false, "Run in admin mode")

	_waitpid = flag.Int("waitpid", 0, "After pid finished, force quit")

	redirectOutput := flag.String("redirect-output", "", "Path to redirect stdout (e.g. named pipe or file)")
	redirectError := flag.String("redirect-error", "", "Path to redirect stderr (e.g. named pipe or file)")

	flag.CommandLine.Parse(os.Args[1:])

	os.Args[0] = *_arg0

	var cachedir string
	cachedir = os.Getenv("NEKOBOX_RULESET_CACHE_DIRECTORY")

	if cachedir == "" {
		cachedir = *_ruleset_cachedir
	}

	internal.SetRulesetCachedir(cachedir)

	if *_admin {
		// works on linux only
		restartAsAdmin(*_save)
	}

	// Redirect stderr and logs if flag is provided
	if *redirectError != "" {
		errFile, err := os.OpenFile(*redirectError, os.O_WRONLY, 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to open error redirect target: %v\n", err)
			os.Exit(1)
		}
		defer errFile.Close()
		os.Stderr = errFile
		log.SetOutput(errFile)
	}

	// Redirect stdout if flag is provided
	if *redirectOutput != "" {
		outFile, err := os.OpenFile(*redirectOutput, os.O_WRONLY, 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to open output redirect target: %v\n", err)
			os.Exit(1)
		}
		defer outFile.Close()
		os.Stdout = outFile
	}

	pid := *_waitpid
	if pid != 0 {
		go func() {
			err := WaitForProcessExit(pid)
			shutdownInstance()
			if err != nil {
				fmt.Println("Error waiting for process:", err)
				os.Exit(1)
			}
			os.Exit(0)
		}()
	}

	runtimeDebug.SetMemoryLimit(2 * 1024 * 1024 * 1024) // 2GB
	go func() {
		var memStats runtime.MemStats
		for {
			time.Sleep(2 * time.Second)
			runtime.ReadMemStats(&memStats)
			if memStats.HeapAlloc > 1.5*1024*1024*1024 {
				// too much memory for sing-box: abort with a clear
				// message instead of a panic stack trace.
				log.Print("FATAL: memory has reached 1.5 GB, this is not normal; exiting")
				os.Exit(1)
			}
		}
	}()

	if runtime.GOOS == "windows" {
		err := checkTaskScheduler(*_save)
		if err != nil {
			if *_debug {
				log.Printf("%v", err)
			}
		}

		if *_admin {
			elevated, _ := isElevated()
			if !elevated {
				code, err := runAdmin()
				if err != nil {
					fmt.Fprintf(os.Stderr, "Failed to run as admin: %v\n", err)
				}
				os.Exit(code)
			}
		}
	}

	fmt.Println("sing-box:", C.Version)
	fmt.Println()

	// The shared test scope is created here; StopTest recreates it via
	// resetTestScope (same funnel, BUG-014).
	resetTestScope()
	var addr net.Addr
	var err error
	socket := *_sock
	if socket != "" {
		goto unix_resolve
	} else {
		if *_port < 0 {
			socket = *_addr
			goto unix_resolve
		}
		listenAddr := *_addr + ":" + strconv.Itoa(*_port)
		addr, err = net.ResolveTCPAddr("tcp", listenAddr)
	}

	goto unix_unresolve
unix_resolve:
	{
		addr, err = net.ResolveUnixAddr("unix", socket)
	}
unix_unresolve:

	if err != nil {
		log.Println("error running thrift server: ", err)
	}
	RunCore(addr, _debug)
}
