package codec

import "io"

type Header struct{
	ServiceMethod string // 方法名 format "Service.Method"
	Seq uint64 // 请求ID，用来区分不同的请求
	Error string
}

type Codec interface {
	io.Closer
	ReadHeader(*Header)error
	ReadBody(interface{})error
	Write(*Header, interface{}) error
}

type NewCodecFunc func(io.ReadWriteCloser) Codec

type Type string

const (
	GobType Type = "application/gob"
	JsonType Type = "application/json"
)

var NewCondcFuncMap map[Type]NewCodecFunc

func init(){
	NewCondcFuncMap = make(map[Type]NewCodecFunc)
	NewCondcFuncMap[GobType] = NewGobCodec
}



