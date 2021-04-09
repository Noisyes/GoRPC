package gorpc

import (
	"GoRPC/codec"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Call struct {
	Seq uint64
	ServiceMethod string
	Args interface{}
	Reply interface{}
	Error error
	Done chan *Call
}

func (call *Call) done(){
	call.Done <- call
}

type Client struct{
	cc codec.Codec
	opt *Option
	sending sync.Mutex
	header codec.Header
	mu sync.Mutex
	seq uint64
	pending map[uint64]*Call
	closing bool
	shutdown bool
}

var ErrShutdown = errors.New("connection is shut down")

func (client *Client) Close() error{
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing{
		return ErrShutdown
	}
	client.closing = true
	return client.cc.Close()
}

func (client *Client) IsAvailable() bool{
	client.mu.Lock()
	defer client.mu.Unlock()
	return !client.shutdown&&!client.closing
}

func (client *Client) registerCall(call *Call)(uint64,error){
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing||client.shutdown{
		return 0,ErrShutdown
	}
	call.Seq = client.seq
	client.pending[call.Seq] = call
	client.seq++
	return call.Seq,nil
}

func (client *Client) removeCall(seq uint64) *Call{
	client.mu.Lock()
	defer client.mu.Unlock()
	call := client.pending[seq]
	delete(client.pending,seq)
	return call
}

func (client *Client) terminateCalls(err error){
	client.sending.Lock()
	defer client.sending.Unlock()
	client.mu.Lock()
	defer client.mu.Unlock()
	client.shutdown = true
	for _,call := range client.pending{
		call.Error = err
		call.done()
	}
}

func (client *Client) receive(){
	var err error
	for err == nil{
		var h codec.Header
		if err = client.cc.ReadHeader(&h); err!=nil{
			break
		}
		call := client.removeCall(h.Seq)
		switch{
		case call == nil:
			err = client.cc.ReadBody(nil)
		case h.Error !="":
			call.Error = fmt.Errorf(h.Error)
			err = client.cc.ReadBody(nil)
			call.done()
		default:
			err = client.cc.ReadBody(call.Reply)
			if err != nil{
				call.Error = fmt.Errorf("reading body" + err.Error())
			}
			call.done()
		}
	}
	client.terminateCalls(err)
}

func NewClient(conn net.Conn, opt *Option)(*Client,error){
	f := codec.NewCondcFuncMap[opt.CodecType]
	if f==nil{
		err := fmt.Errorf("invalid codec type %s",opt.CodecType)
		log.Println("rpc client: codec error:",err)
		return nil,err
	}
	if err := json.NewEncoder(conn).Encode(opt);err!=nil{
		log.Println("rpc client:options error:",err)
		conn.Close()
		return nil,err
	}
	return NewClientCodec(f(conn),opt),nil
}

func NewClientCodec(cc codec.Codec,opt *Option) *Client{
	client := &Client{
		seq:1,
		cc:cc,
		opt: opt,
		pending: make(map[uint64]*Call),
	}
	go client.receive()
	return client
}

func parseOptions(opts ...*Option)(*Option,error){
	if len(opts) == 0||opts[0]==nil{
		return DefaultOption,nil
	}
	if len(opts)!=1{
		return nil,errors.New("number of options is more than one")
	}
	opt := opts[0]
	opt.MagicNumber = DefaultOption.MagicNumber
	if opt.CodecType == ""{
		opt.CodecType = DefaultOption.CodecType
	}
	return opt,nil
}

type clientResult struct{
	client *Client
	err error
}

type newClientFunc func(conn net.Conn, opt *Option)(client *Client,err error)

func dialTimeout(f newClientFunc,network,addr string,opts ...*Option)(client *Client,err error){
	opt,err := parseOptions(opts...)
	if err !=nil{
		return nil,err
	}
	conn,err := net.DialTimeout(network,addr,opt.ConnectTimeout)
	if err!=nil{
		log.Println("conection timeout")
		return nil,err
	}
	defer func(){
		if err != nil{
			conn.Close()
		}
	}()
	ch := make(chan clientResult)
	go func(){
		client,err := f(conn,opt)
		ch<-clientResult{client: client,err: err}
	}()
	if opt.ConnectTimeout == 0{
		result := <-ch
		return result.client, result.err
	}
	select{
		case<-time.After(opt.ConnectTimeout):
			return nil,fmt.Errorf("rpc client: connect timeout: expect within %s",opt.ConnectTimeout)
		case result:= <-ch:
			return result.client,result.err
	}

}

func Dial(network,addr string,opts ...*Option)(client *Client,err error){
	return dialTimeout(NewClient,network,addr,opts...)
}

func (client *Client) send(call *Call){
	client.sending.Lock()
	defer client.sending.Unlock()

	seq,err := client.registerCall(call)
	if err !=nil{
		call.Error = err
		call.done()
		return
	}

	client.header.ServiceMethod = call.ServiceMethod
	client.header.Seq = seq
	client.header.Error = ""

	if err:= client.cc.Write(&client.header,call.Args);err !=nil{
		call := client.removeCall(seq)
		if call !=nil{
			call.Error = err
			call.done()
		}
	}
}

func (client *Client) Go(serviceMethod string,args,reply interface{},done chan *Call)*Call{
	if done == nil{
		done = make(chan *Call,10)
	}else if cap(done)==0{
		log.Panic("rpc client: done channel is unbuffered")
	}

	call := &Call{
		ServiceMethod:  serviceMethod,
		Args: args,
		Reply: reply,
		Done: done,
	}
	client.send(call)
	return call
}

func (client *Client) Call(ctx context.Context,serviceMethod string, args,reply interface{}) error{
	call := client.Go(serviceMethod,args,reply,make(chan *Call,1))
	select{
	case <-ctx.Done():
		client.removeCall(call.Seq)
		return errors.New("rpc client: call fialed: "+ ctx.Err().Error())
		case call = <-call.Done:
			return call.Error
	}
	return call.Error
}


func NewHTTPClient(conn net.Conn,opt *Option)(*Client,error){
	io.WriteString(conn,fmt.Sprintf("CONNECT %s HTTP/1.0\r\n\r\n",defaultRPCPath))
	resp, err := http.ReadResponse(bufio.NewReader(conn),&http.Request{Method: "CONNECT"})
	defer resp.Body.Close()
	if err == nil &&resp.Status == connected{
		return NewClient(conn,opt)
	}
	if err ==nil{
		err = errors.New("unexpected HTTP response:"+ resp.Status)
	}
	return nil,err
}

func DialHTTP(network,address string,opt ...*Option)(*Client,error){
	return dialTimeout(NewHTTPClient,network,address,opt...)
}

func XDial(rpcAddr string,opts...*Option)(*Client,error){
	parts := strings.Split(rpcAddr,"@")
	if len(parts)!=2{
		return nil,fmt.Errorf("rpc client err:wrong format '%s',expect protocol@addr",rpcAddr)
	}
	protocol,addr := parts[0],parts[1]
	switch protocol{
	case "http":
		return DialHTTP("tcp",addr,opts...)
	default:
		return Dial(protocol,addr,opts...)
	}
}


