package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/golang-jwt/jwt"
	"hash/crc32"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
)

type CustomClaims struct {
	Email  string `json:"email,omitempty"`
	Mobile string `json:"mobile,omitempty"`
	Name   string `json:"name,omitempty"`
	UserId string `json:"userId,omitempty"`
	jwt.StandardClaims
}

type ServiceInfo struct {
	IP             string
	Port           int
	CpuLoad        float64 `json:"CpuLoad"`
	TotalMem       uint64  `json:"TotalMem"`
	FreeMem        uint64  `json:"FreeMem"`
	UsedPercentMem float64 `json:"UsedPercentMem"`
	Timestamp      int     `json:"ts"`
}

type Authentication struct {
	Method        string
	Secret        string
	RequiredField string
	LoginUrl      string
}

type ServiceList []ServiceInfo
type Array []string

// Value ...
func (i *Array) String() string {
	return fmt.Sprint(*i)
}

// Set 方法是flag.Value接口, 设置flag Value的方法.
// 通过多个flag指定的值， 所以我们追加到最终的数组上.
func (i *Array) Set(value string) error {
	*i = append(*i, value)
	return nil
}

type Value interface {
	String() string
	Set(string) error
}
type ResponseOfMethods struct {
	Code int               `json:"code"`
	Data map[string]string `json:"data"`
}
type WuJingHttpHandler map[string]string

var (
	serviceMap    = make(map[string]ServiceList)
	HashMethodMap = make(map[string]string)
	ruleMap       = make(map[string]Authentication)
	methodLocker  = new(sync.Mutex)
)

const (
	UrlHash   = "UrlHash"
	IPHash    = "IPHash"
	RandHash  = "RandHash"
	LoadRound = "LoadRound"
)

func Quit() {
	os.Exit(0)
}
func HandleOsKill() {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Kill, os.Interrupt)
	<-quit
	fmt.Println("killing signal")
	Quit()
}

//解析token
func ParseToken(tokenString string, secret string) (*CustomClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &CustomClaims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("Unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	})
	if claims, ok := token.Claims.(*CustomClaims); ok && token.Valid {
		return claims, nil
	} else {
		return nil, err
	}
}

func CheckErr(err error) {
	if err != nil {
		log.Printf("error: %v", err)
		os.Exit(1)
	}
}
func StatusHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println("check status.")
	fmt.Fprint(w, "status ok!")
}

func StartProxyService(addr string) {
	fmt.Println("start listen..." + addr)
	handler := WuJingHttpHandler{}
	err := http.ListenAndServe(addr, handler)
	CheckErr(err)
}
func Normalize(hostname string) string {
	return strings.ToLower(hostname)
	//return strings.ReplaceAll(hostname, ".", "-")
}

func GetAllBackends(hostname string) string {
	data := serviceMap[Normalize(hostname)]
	if data == nil {
		return ""
	}
	if len(data) == 0 {
		return ""
	}
	jsonString, er := json.Marshal(data)
	if er == nil {
		return string(jsonString)
	}
	return ""
}
func GetBackendServerByHostName(hostnameOriginal string, ip string, path string) string {
	hostname := Normalize(hostnameOriginal)
	data := serviceMap[Normalize(hostname)]
	if data == nil {
		log.Println("map item backend-" + hostname + " is null")
		return ""
	}
	if len(data) == 0 {
		log.Println("map lenth of  backend-" + hostname + " is 0")
		return ""
	}

	method, ok := HashMethodMap[hostname]
	if !ok {
		method = RandHash
	}
	var server ServiceInfo
	/**
	随机分一台
	*/
	if method == RandHash {
		idx := rand.Intn(len(data))
		server = data[idx]
	}
	/**
	找出负载最低的那一台;
	*/
	if method == LoadRound {
		maxLoad := float64(1000000)
		for i := 0; i < len(data); i++ {
			if data[i].CpuLoad < maxLoad {
				server = data[i]
				maxLoad = data[i].CpuLoad
			}
		}
	}
	/**
	根据IP或是UrlHash Hash一台出来；
	*/
	if method == IPHash || method == UrlHash {
		var seed string
		if method == IPHash {
			seed = ip
		}
		if method == UrlHash {
			seed = path
		}
		crc32q := crc32.MakeTable(0xD5828281)
		checkSum := crc32.Checksum([]byte(seed), crc32q)
		idx := checkSum % uint32(len(data))
		server = data[idx]
	}
	return fmt.Sprintf("%s:%d", server.IP, server.Port)
}

func showHashMethodsHandle(w http.ResponseWriter, r *http.Request) {
	var response ResponseOfMethods
	response.Data = HashMethodMap
	response.Code = 200
	jsonTxt, er := json.Marshal(response)
	if er != nil {
		w.Write([]byte("{'code':200,'msg':'json encode failed'}"))
		return
	} else {
		w.Write([]byte(jsonTxt))
	}
}
func updateHashHandle(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	domain := ""
	method := ""
	if r.Form["domain"] != nil {
		domain = strings.Join(r.Form["domain"], "")
	}
	if r.Form["method"] != nil {
		method = strings.Join(r.Form["method"], "")
	}

	if method != RandHash && method != IPHash && method != UrlHash && method != LoadRound {
		w.Write([]byte("{'code':200,'msg':'method invalid'}"))
		return
	}
	if domain != "" && method != "" {
		methodLocker.Lock()
		HashMethodMap[domain] = method
		methodLocker.Unlock()
	}
	w.Write([]byte("{'code':200}"))
}

func GetBackendsHandle(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	runes := []rune(path)
	start := len("/__backend/")
	queryHost := string(runes[start:len(path)])
	backends := GetAllBackends(queryHost)
	w.Write([]byte(backends))
}

func HandleAllBackends(w http.ResponseWriter, r *http.Request) {
	_data, er := json.Marshal(serviceMap)
	if er != nil {
		msg := "{'code':401,msg:'cna't get message'}"
		w.Write([]byte(msg))
		return
	}
	w.Write(_data)
}

func redirect(w http.ResponseWriter, r *http.Request, redirectUrl string) {
	http.Redirect(w, r, redirectUrl, http.StatusFound)
}
func (self WuJingHttpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	hostSeg := r.Host
	idx := strings.Index(hostSeg, ":")
	if idx < 0 {
		idx = 0
	}
	runes := []rune(hostSeg)
	queryHost := string(runes[0:idx])
	if queryHost == "" {
		queryHost = hostSeg
	}

	if strings.HasPrefix(r.URL.Path, "/__backends") {
		HandleAllBackends(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/__status") {
		StatusHandler(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/__backend/") {
		GetBackendsHandle(w, r)
		return
	}

	if strings.HasPrefix(r.URL.Path, "/__hashMethods") {
		showHashMethodsHandle(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/__hashMethod") {
		updateHashHandle(w, r)
		return
	}

	hostRule, okRule := ruleMap[queryHost]
	if !okRule {
		log.Printf("Authentication Rule of %v not exists", queryHost)
	} else {

	}
	cookieToken, er := r.Cookie("_wjToken")
	if er != nil {
		log.Printf("fetch wjCookie failed: host:%v,path:%v", r.Host, r.URL.Path)
		redirect(w, r, hostRule.LoginUrl)
		return
	}

	jwtToken, errToken := ParseToken(cookieToken.Value, hostRule.Secret)
	if errToken != nil {
		log.Printf("jwt Token parse failed:%v,host:%v,path:%v,error:%v",
			cookieToken.Value, r.Host, r.URL.Path, errToken)
		redirect(w, r, hostRule.LoginUrl)
	} else {
		log.Printf("jwt token parsed,host:%v,path:%v,token:%v", r.Host, r.URL.Path, jwtToken)
	}

	if hostRule.RequiredField == "Name" || hostRule.RequiredField == "name" {
		if len(jwtToken.Name) == 0 {
			w.WriteHeader(302)
			http.Redirect(w, r, hostRule.LoginUrl, http.StatusFound)
			return
		}
	}

	if hostRule.RequiredField == "Email" || hostRule.RequiredField == "email" {
		if len(jwtToken.Email) == 0 {
			w.WriteHeader(302)
			http.Redirect(w, r, hostRule.LoginUrl, http.StatusFound)
			return
		}
	}

	if hostRule.RequiredField == "UserId" || hostRule.RequiredField == "userId" {
		if len(jwtToken.UserId) == 0 {
			w.WriteHeader(302)
			http.Redirect(w, r, hostRule.LoginUrl, http.StatusFound)
			return
		}
	}

	if hostRule.RequiredField == "Mobile" || hostRule.RequiredField == "mobile" {
		if len(jwtToken.Mobile) == 0 {
			w.WriteHeader(302)
			http.Redirect(w, r, hostRule.LoginUrl, http.StatusFound)
			return
		}
	}

	if hostRule.RequiredField == "Subject" || hostRule.RequiredField == "subject" {
		if len(jwtToken.Subject) == 0 {
			w.WriteHeader(302)
			http.Redirect(w, r, hostRule.LoginUrl, http.StatusFound)
			return
		}
	}

	var ip string

	if len(r.Header["X-Real-Ip"]) < 1 {
		log.Printf("without X-Real-Ip,")
		ip = ""
	} else {
		ip = r.Header["X-Real-Ip"][0]
	}

	log.Printf("query backend for host:" + queryHost + ",ip:" + ip + ",path:" + r.URL.Path)
	backend := GetBackendServerByHostName(queryHost, ip, r.URL.Path)
	if backend == "" {
		w.WriteHeader(504)
		return
	}

	peer, err := net.Dial("tcp", backend)
	if err != nil {
		log.Printf("dial upstream error:%v", err)
		w.WriteHeader(503)
		return
	}
	if err := r.Write(peer); err != nil {
		log.Printf("write request to upstream error :%v", err)
		w.WriteHeader(502)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		w.WriteHeader(500)
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		w.WriteHeader(500)
		return
	}
	log.Printf(
		"serving %s < %s <-> %s > %s ",
		peer.RemoteAddr(), peer.LocalAddr(),
		conn.RemoteAddr(), conn.LocalAddr(),
	)

	go func() {
		defer func(peer net.Conn) {
			err := peer.Close()
			if err != nil {

			}
		}(peer)
		defer func(conn net.Conn) {
			err := conn.Close()
			if err != nil {

			}
		}(conn)
		_, err := io.Copy(peer, conn)
		if err != nil {
			return
		}
	}()
	go func() {
		defer func(peer net.Conn) {
			err := peer.Close()
			if err != nil {

			}
		}(peer)
		defer func(conn net.Conn) {
			err := conn.Close()
			if err != nil {

			}
		}(conn)
		_, err := io.Copy(conn, peer)
		if err != nil {
			return
		}
	}()
}

func main() {
	var proxyAddr string
	var errorLogFile string
	var testOnly bool

	var mapFile string
	var ruleFile string
	flag.StringVar(&mapFile, "map_file", "./map.json", " the json file of service map")
	flag.StringVar(&ruleFile, "rule_file", "./rule.json", "rule json file path")
	flag.BoolVar(&testOnly, "test", false, "test mode; parse the serviceMap file")
	flag.StringVar(&proxyAddr, "proxy_addr", "0.0.0.0:5577", "start a proxy and transfer to backend")
	flag.StringVar(&errorLogFile, "error_log", "/var/log/wujing/wujing.error.log", "log file position")
	flag.Parse()

	f, err := os.OpenFile(errorLogFile, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0755)
	if err != nil {
		log.Fatalf("error opening file: %v,%v", errorLogFile, err)
	}
	defer func(f *os.File) {
		err := f.Close()
		if err != nil {

		}
	}(f)
	log.SetOutput(f)
	_, errOfStat := os.Stat(mapFile)
	if errOfStat != nil {
		if !os.IsExist(err) {
			log.Fatalf("mapFile not exists or can't be stat:%v", mapFile)
		}
	} else {

	}
	jsonData, readErr := ioutil.ReadFile(mapFile)
	if readErr != nil {
		log.Fatalf("mapFile read failed:%v", mapFile)
	}
	json.Unmarshal(jsonData, &serviceMap)

	ruleData, readRuleErr := ioutil.ReadFile(ruleFile)
	if readRuleErr != nil {
		log.Fatalf("hostRule fail failed:")
	}
	json.Unmarshal(ruleData, &ruleMap)
	log.Printf("json map :%v", serviceMap)
	log.Printf("rule map:%v", ruleMap)

	go HandleOsKill()

	log.Println("proxy mode")
	/**
	开启Proxy
	*/
	go StartProxyService(proxyAddr)
	select {}
}
