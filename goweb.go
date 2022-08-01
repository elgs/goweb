package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"syscall"
)

var servers []Server

func main() {
	confPath := flag.String("c", "goweb.json", "configration file path")
	flag.Parse()
	confBytes, err := ioutil.ReadFile(*confPath)
	if err != nil {
		fmt.Println(err)
		return
	}
	servers, err = NewConfig(confBytes)
	if err != nil {
		fmt.Println(err)
		return
	}

	for serverIndex := range servers {
		server := servers[serverIndex]
		if server.Disabled {
			continue
		}
		err := server.Start()
		if err != nil {
			log.Fatal(err)
		}
	}

	Hook(nil)
}

func Hook(clean func()) {
	sigs := make(chan os.Signal, 1)
	done := make(chan bool, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		select {
		case sig := <-sigs:
			fmt.Println(sig)
			if clean != nil {
				clean()
			}
			done <- true
		}
	}()
	<-done
}
