package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	color "github.com/fatih/color"
)

type DNSLookup struct {
	DomainName string
	Nameserver string
	IPAddrs    []string
}

type ProgArgs struct {
	DomainFile          string
	NSFile              string
	OutputFile          string
	UpdateCloudServices bool
}

func (dns *DNSLookup) DoLookup() (*DNSLookup, error) {
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{
				Timeout: time.Millisecond * time.Duration(10000),
			}
			return d.DialContext(ctx, network, fmt.Sprintf("%s:53", dns.Nameserver))
		},
	}

	var err error
	dns.IPAddrs, err = r.LookupHost(context.Background(), dns.DomainName)

	return dns, err
}

type CloudServices struct {
	Services []CloudService
}

type CloudService struct {
	Name    string
	IPRange []string
}

func (c *CloudServices) IsCloud(ip string) bool {
	for _, service := range c.Services {
		for _, rangeIP := range service.IPRange {
			if rangeIP == ip {
				return true
			}
		}
	}
	return false
}

func (c *CloudServices) ReadCloudServices() (CloudServices, error) {

	// Read the Cloud Services IP Ranges
	localFile := "ip-ranges.json"

	file, err := os.Open(localFile)
	if err != nil {
		panic(err)
	}
	defer file.Close()

	data, err := ioutil.ReadAll(file)
	if err != nil {
		panic(err)
	}

	err = json.Unmarshal(data, c)
	if err != nil {
		panic(err)
	}

	return *c, nil
}

func (c *CloudServices) IsCloudIP(ip net.IP) (bool, string, error) {
	for _, service := range c.Services {
		for _, rangeIP := range service.IPRange {
			_, ipRange, _ := net.ParseCIDR(rangeIP)
			if ipRange.Contains(ip) {
				return true, service.Name, nil
			}
		}
	}
	return false, "", nil
}

func (c *CloudServices) UpdateCloudServices() {
	// Fetch the Cloud Services IP Ranges

	localFile := "ip-ranges.json"

	// create local file for writing
	file, err := os.Create(localFile)
	if err != nil {
		panic(err)
	}
	defer file.Close()

	urls := make(map[string]string)
	urls["AWS"] = "https://ip-ranges.amazonaws.com/ip-ranges.json"
	urls["Cloudflare"] = "https://www.cloudflare.com/ips-v4"
	urls["Cloudflare6"] = "https://www.cloudflare.com/ips-v6"
	urls["Azure"] = "https://download.microsoft.com/download/7/1/D/71D86715-5596-4529-9B13-DA13A5DE5B63/ServiceTags_Public_20221212.json"

	var wg sync.WaitGroup
	// Parse the JSON
	for name, url := range urls {
		wg.Add(1)

		go func(name string, url string) {
			defer wg.Done()

			res, err := http.Get(url)
			fmt.Println("[+] Fetching IP ranges for", name)
			if err != nil {
				panic(err)
			}

			switch name {
			case "AWS":
				type AWSIPRanges struct {
					SyncToken  string `json:"syncToken"`
					CreateDate string `json:"createDate"`
					Prefixes   []struct {
						IPPrefix string `json:"ip_prefix"`
						Region   string `json:"region"`
						Service  string `json:"service"`
					} `json:"prefixes"`
					Ipv6Prefixes []struct {
						IPPrefix string `json:"ipv6_prefix"`
						Region   string `json:"region"`
						Service  string `json:"service"`
					} `json:"ipv6_prefixes"`
				}

				var aws AWSIPRanges
				err = json.NewDecoder(res.Body).Decode(&aws)
				if err != nil {
					panic(err)
				}
				var ipv4 []string
				for _, prefix := range aws.Prefixes {
					ipv4 = append(ipv4, prefix.IPPrefix)
				}
				c.Services = append(c.Services, CloudService{Name: name, IPRange: ipv4})

				var ipv6 []string
				for _, prefix := range aws.Ipv6Prefixes {
					ipv6 = append(ipv6, prefix.IPPrefix)
				}
				c.Services = append(c.Services, CloudService{Name: name, IPRange: ipv6})

			case "Cloudflare":
			case "Cloudflare6":
				scanner := bufio.NewScanner(res.Body)
				scanner.Split(bufio.ScanLines)
				var ips []string
				for scanner.Scan() {
					ips = append(ips, scanner.Text())
				}
				c.Services = append(c.Services, CloudService{Name: name, IPRange: ips})
			case "Azure":

				type AzureIPRanges struct {
					Values []struct {
						Name       string `json:"name"`
						ID         string `json:"id"`
						Location   string `json:"location"`
						Properties struct {
							AddressPrefixes []string `json:"addressPrefixes"`
							Platform        string   `json:"platform"`
						} `json:"properties"`
					} `json:"values"`
				}

				var azure AzureIPRanges
				err = json.NewDecoder(res.Body).Decode(&azure)
				if err != nil {
					panic(err)
				}

				for _, prefix := range azure.Values {
					c.Services = append(c.Services, CloudService{Name: name, IPRange: prefix.Properties.AddressPrefixes})
				}
			}
		}(name, url)
	}
	wg.Wait()

	// Write the results to the file in JSON format
	b, err := json.MarshalIndent(c, "", "	")
	if err != nil {
		panic(err)
	}

	_, err = file.Write(b)
	if err != nil {
		panic(err)
	}

}

func main() {

	args := ProgArgs{}

	// Create a flag to read the domain name from a file or from the command line
	flag.StringVar(&args.DomainFile, "df", "", "File containing domains to lookup")
	flag.StringVar(&args.NSFile, "nf", "nameservers.txt", "File containing nameservers to use for lookup")
	flag.StringVar(&args.OutputFile, "o", "", "Output File (JSON)")
	flag.BoolVar(&args.UpdateCloudServices, "update", false, "Update the cloud service IP ranges")
	flag.Parse()

	cloud := CloudServices{}

	if args.UpdateCloudServices {
		cloud.UpdateCloudServices()
	} else {
		cloud.ReadCloudServices()
	}

	green := color.New(color.FgGreen, color.Bold)
	red := color.New(color.FgRed, color.Bold)

	// Read the nameservers from the file
	nameserverFile, err := os.Open(args.NSFile)
	if err != nil {
		panic(err)
	}

	nsScanner := bufio.NewScanner(nameserverFile)
	nsScanner.Split(bufio.ScanLines)
	var nameservers []string
	for nsScanner.Scan() {
		nameservers = append(nameservers, nsScanner.Text())
	}
	nameserverFile.Close()

	// Read the domain names from the file
	domainFile, err := os.Open(args.DomainFile)
	if err != nil {
		panic(err)
	}

	var wg sync.WaitGroup
	dfScanner := bufio.NewScanner(domainFile)
	dfScanner.Split(bufio.ScanLines)
	for dfScanner.Scan() {
		wg.Add(1)
		go func(domain string) {
			defer wg.Done()

			d := DNSLookup{}
			d.DomainName = strings.TrimSpace(domain)
			d.Nameserver = nameservers[rand.Intn(len(nameservers))]

			res, err := d.DoLookup()

			if err != nil {
				fmt.Println(err)
			} else {
				for _, ip := range res.IPAddrs {

					isCloud, service, err := cloud.IsCloudIP(net.ParseIP(ip))

					if err != nil {
						fmt.Println(err)
					} else {
						if isCloud {
							green.Printf("[+] Is Cloud Service: %t | Service: %s | IP: %s | Domain: %s\n", isCloud, service, ip, domain)
						} else {
							red.Printf("[-] Is Cloud Service: %t | IP: %s | Domain: %s\n", isCloud, ip, domain)
						}
					}
				}
			}
		}(dfScanner.Text())

		wg.Wait()
	}

	domainFile.Close()
}
