package xclient

import (
	"log"
	"net/http"
	"strings"
	"time"
)

type GoRegistryDiscovery struct{
	*MultiServersDiscovery
	registry string
	timeout time.Duration
	lastUpdate time.Time
}

const defaultUpdateTimeout = time.Second * 10

func NewGoRegistryDiscovery(registerAddr string,timeout time.Duration) *GoRegistryDiscovery{
	if timeout == 0{
		timeout = defaultUpdateTimeout
	}
	d := &GoRegistryDiscovery{
		MultiServersDiscovery:NewMultiServersDiscovery(make([]string,10)),
		registry: registerAddr,
		timeout: timeout,
	}
	return d
}


func (d *GoRegistryDiscovery) Update(servers []string) error{
	d.mu.Lock()
	defer d.mu.Unlock()
	d.servers = servers
	d.lastUpdate = time.Now()
	return nil
}

func (d *GoRegistryDiscovery) Refresh()error{
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lastUpdate.Add(d.timeout).After(time.Now()){
		return nil
	}
	log.Println("rpc registry: refresh servers from registry",d.registry)
	res,err := http.Get(d.registry)
	if err != nil{
		log.Println("rpc registry refresh err")
		return err
	}
	servers := strings.Split(res.Header.Get("X-Gorpc-servers"),",")
	d.servers = make([]string,0,len(servers))
	for _,server := range servers{
		if strings.TrimSpace(server)!=""{
			d.servers = append(d.servers,strings.TrimSpace(server))
		}
	}
	d.lastUpdate = time.Now()
	return nil
}


func (d *GoRegistryDiscovery) Get(mode SelectMode)(string,error){
	if err := d.Refresh();err!=nil{
		return "",err
	}
	return d.MultiServersDiscovery.Get(mode)
}

func (d *GoRegistryDiscovery) GetAll()([]string,error){
	if err := d.Refresh();err!=nil{
		return nil,err
	}
	return d.MultiServersDiscovery.GetAll()
}


