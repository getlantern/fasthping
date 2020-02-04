package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/getlantern/shortcut"
)

var (
	excludeHosts    = flag.String("eh", "", "The path of the file containing list of hosts to exclude from the candidates.")
	excludeIPRanges = flag.String("ei", "", "The path of the file containing list of IP ranges to exclude from the candidates. IP ranges should be in CIDR notation.")
	interval        = flag.Duration("i", 0, "The interval to wait before ping the next candidate. No wait if absent.")
	timeout         = flag.Duration("t", 0, "timeout before giving up ping a host. System default if absent.")
	workers         = flag.Int("w", runtime.NumCPU(), "Number of workers. Match the CPU count if absent.")

	hosts map[string]struct{}
	nets  *shortcut.SortList
)

func main() {
	flag.Parse()
	hosts = make(map[string]struct{})
	if *excludeHosts != "" {
		for _, h := range readLines(*excludeHosts) {
			hosts[h] = struct{}{}
		}
	}
	if *excludeIPRanges != "" {
		nets = shortcut.NewSortList(readLines(*excludeIPRanges))
	} else {
		nets = shortcut.NewSortList([]string{})
	}
	ch := make(chan string)
	go func() {
		var next <-chan time.Time
		if *interval > 0 {
			next = time.NewTicker(*interval).C
		} else {
			c := make(chan time.Time)
			close(c)
			next = c
		}
		for l := range iterateLines(os.Stdin) {
			if _, exists := hosts[l]; exists {
				continue
			}
			<-next
			ch <- l
		}
		close(ch)
	}()

	log.Printf("Spawning %d workers\n", *workers)
	var wg sync.WaitGroup
	wg.Add(*workers)
	for i := 0; i < *workers; i++ {
		go worker(ch, &wg)
	}
	wg.Wait()
}

func readLines(fname string) []string {
	result := []string{}
	f, err := os.Open(fname)
	if err != nil {
		log.Fatalf("Can not open %s: %v", fname, err)
	}
	defer f.Close()
	for l := range iterateLines(f) {
		result = append(result, l)
	}
	return result
}

func iterateLines(f *os.File) <-chan string {
	ch := make(chan string)
	go func() {
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			ch <- scanner.Text()
		}
		close(ch)
	}()
	return ch
}

func worker(ch chan string, wg *sync.WaitGroup) {
	for candidate := range ch {
		one(candidate)
	}
	wg.Done()
}

func one(host string) {
	ctx := context.Background()
	cancel := func() {}
	if *timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, *timeout)
	}
	defer cancel()
	if ip := resolve(ctx, host); ip != nil {
		ping(ctx, ip, host)
	}
}

func resolve(ctx context.Context, host string) net.IP {
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		log.Printf("Error lookup %v: %v", host, err)
		return nil
	}
	ip := ips[0].IP
	if nets.Contains(ip) {
		return nil
	}
	return ip
}

func ping(ctx context.Context, ip net.IP, host string) {
	req, err := http.NewRequest(http.MethodHead, "https://"+host, nil)
	if err != nil {
		log.Println(err)
		return
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, net.JoinHostPort(ip.String(), "https"))
		},
	}
	resp, err := tr.RoundTrip(req.WithContext(ctx))
	if err != nil {
		log.Printf("Error round trip to %s(%v): %v\n", host, ip, err)
		return
	}
	resp.Body.Close()
	fmt.Println(host)
}
