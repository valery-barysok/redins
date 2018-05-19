package handler

import (
    "encoding/json"
    "strings"
    "time"
    "log"
    "math/rand"
    "sync"

    "github.com/miekg/dns"
    "arvancloud/redins/redis"
    "github.com/patrickmn/go-cache"
    "arvancloud/redins/config"
    "arvancloud/redins/eventlog"
    "arvancloud/redins/dns_types"
)

type DnsRequestHandler struct {
    DefaultTtl     int
    ZoneReload     int
    CacheTimeout   int
    Zones          []string
    zoneLock       sync.RWMutex
    LastZoneUpdate time.Time
    Redis          *redis.Redis
    Logger         *eventlog.EventLogger
    cache          *cache.Cache
}

type Zone struct {
    Name      string
    Locations map[string]struct{}
}

func NewHandler(config *config.RedinsConfig) *DnsRequestHandler {
    h := &DnsRequestHandler {
        DefaultTtl: config.Handler.DefaultTtl,
        ZoneReload: config.Handler.ZoneReload,
        CacheTimeout: config.Handler.CacheTimeout,
        zoneLock: sync.RWMutex{},
    }

    h.Redis = redis.NewRedis(&config.Handler.Redis)
    h.Logger = eventlog.NewLogger(&config.Handler.Log)

    h.LoadZones()

    h.cache = cache.New(time.Second * time.Duration(h.CacheTimeout), time.Duration(h.CacheTimeout) * time.Second * 10)

    return h
}

func (h *DnsRequestHandler) LoadZones() {
    h.LastZoneUpdate = time.Now()
    newZones := h.Redis.GetKeys()
    h.zoneLock.Lock()
    h.Zones = newZones
    h.zoneLock.Unlock()
}

func (h *DnsRequestHandler) FetchRecord(qname string) (*dns_types.Record, int) {
    record, found := h.cache.Get(qname)
    if found {
        // log.Println("[DEBUG] cached")
        return record.(*dns_types.Record), dns.RcodeSuccess
    }
    record, res := h.GetRecord(qname)
    if res == dns.RcodeSuccess {
        h.cache.Set(qname, record, time.Duration(h.CacheTimeout) * time.Second)
        return record.(*dns_types.Record), dns.RcodeSuccess
    }
    return nil, res
}

func (h *DnsRequestHandler) A(name string, ips []dns_types.IP_Record) (answers []dns.RR) {
    for _, ip := range ips {
        if ip.Ip == nil {
            continue
        }
        r := new(dns.A)
        r.Hdr = dns.RR_Header{Name: name, Rrtype: dns.TypeA,
            Class: dns.ClassINET, Ttl: h.minTtl(ip.Ttl)}
        r.A = ip.Ip
        answers = append(answers, r)
    }
    return
}

func (h *DnsRequestHandler) AAAA(name string, ips []dns_types.IP_Record) (answers []dns.RR) {
    for _, ip := range ips {
        if ip.Ip == nil {
            continue
        }
        r := new(dns.AAAA)
        r.Hdr = dns.RR_Header{Name: name, Rrtype: dns.TypeAAAA,
            Class: dns.ClassINET, Ttl: h.minTtl(ip.Ttl)}
        r.AAAA = ip.Ip
        answers = append(answers, r)
    }
    return
}

func (h *DnsRequestHandler) CNAME(name string, record *dns_types.Record) (answers []dns.RR) {
    for _, cname := range record.CNAME {
        if len(cname.Host) == 0 {
            continue
        }
        r := new(dns.CNAME)
        r.Hdr = dns.RR_Header{Name: name, Rrtype: dns.TypeCNAME,
            Class: dns.ClassINET, Ttl: h.minTtl(cname.Ttl)}
        r.Target = dns.Fqdn(cname.Host)
        answers = append(answers, r)
    }
    return
}

func (h *DnsRequestHandler) TXT(name string, record *dns_types.Record) (answers []dns.RR) {
    for _, txt := range record.TXT {
        if len(txt.Text) == 0 {
            continue
        }
        r := new(dns.TXT)
        r.Hdr = dns.RR_Header{Name: name, Rrtype: dns.TypeTXT,
            Class: dns.ClassINET, Ttl: h.minTtl(txt.Ttl)}
        r.Txt = split255(txt.Text)
        answers = append(answers, r)
    }
    return
}

func (h *DnsRequestHandler) NS(name string, record *dns_types.Record) (answers []dns.RR) {
    for _, ns := range record.NS {
        if len(ns.Host) == 0 {
            continue
        }
        r := new(dns.NS)
        r.Hdr = dns.RR_Header{Name: name, Rrtype: dns.TypeNS,
            Class: dns.ClassINET, Ttl: h.minTtl(ns.Ttl)}
        r.Ns = ns.Host
        answers = append(answers, r)
    }
    return
}

func (h *DnsRequestHandler) MX(name string, record *dns_types.Record) (answers []dns.RR) {
    for _, mx := range record.MX {
        if len(mx.Host) == 0 {
            continue
        }
        r := new(dns.MX)
        r.Hdr = dns.RR_Header{Name: name, Rrtype: dns.TypeMX,
            Class: dns.ClassINET, Ttl: h.minTtl(mx.Ttl)}
        r.Mx = mx.Host
        r.Preference = mx.Preference
        answers = append(answers, r)
    }
    return
}

func (h *DnsRequestHandler) SRV(name string, record *dns_types.Record) (answers []dns.RR) {
    for _, srv := range record.SRV {
        if len(srv.Target) == 0 {
            continue
        }
        r := new(dns.SRV)
        r.Hdr = dns.RR_Header{Name: name, Rrtype: dns.TypeSRV,
            Class: dns.ClassINET, Ttl: h.minTtl(srv.Ttl)}
        r.Target = srv.Target
        r.Weight = srv.Weight
        r.Port = srv.Port
        r.Priority = srv.Priority
        answers = append(answers, r)
    }
    return
}

func (h *DnsRequestHandler) SOA(name string, record *dns_types.Record) (answers []dns.RR) {
    r := new(dns.SOA)
    if record.SOA.Ns == "" {
        r.Hdr = dns.RR_Header{Name: name, Rrtype: dns.TypeSOA,
            Class: dns.ClassINET, Ttl: uint32(h.DefaultTtl)}
        r.Ns = "ns1." + name
        r.Mbox = hostmaster + "." + name
        r.Refresh = 86400
        r.Retry = 7200
        r.Expire = 3600
        r.Minttl = uint32(h.DefaultTtl)
    } else {
        r.Hdr = dns.RR_Header{Name: record.ZoneName, Rrtype: dns.TypeSOA,
            Class: dns.ClassINET, Ttl: h.minTtl(record.SOA.Ttl)}
        r.Ns = record.SOA.Ns
        r.Mbox = record.SOA.MBox
        r.Refresh = record.SOA.Refresh
        r.Retry = record.SOA.Retry
        r.Expire = record.SOA.Expire
        r.Minttl = record.SOA.MinTtl
    }
    r.Serial = h.serial()
    answers = append(answers, r)
    return
}

func (h *DnsRequestHandler) serial() uint32 {
    return uint32(time.Now().Unix())
}

func (h *DnsRequestHandler) minTtl(ttl uint32) uint32 {
    defaultTtl := uint32(h.DefaultTtl)
    if defaultTtl == 0 && ttl == 0 {
        return defaultTtl
    }
    if defaultTtl == 0 {
        return ttl
    }
    if ttl == 0 {
        return defaultTtl
    }
    if defaultTtl < ttl {
        return defaultTtl
    }
    return ttl
}

func (h *DnsRequestHandler) findLocation(query string, z *Zone) string {
    var (
        ok                bool
        closestEncloser   string
        sourceOfSynthesis string
    )

    // request for zone records
    if query == z.Name {
        return query
    }

    query = strings.TrimSuffix(query, "."+z.Name)

    if _, ok = z.Locations[query]; ok {
        return query
    }

    closestEncloser, sourceOfSynthesis, ok = splitQuery(query)
    for ok {
        ceExists := keyMatches(closestEncloser, z) || keyExists(closestEncloser, z)
        ssExists := keyExists(sourceOfSynthesis, z)
        if ceExists {
            if ssExists {
                return sourceOfSynthesis
            } else {
                return ""
            }
        } else {
            closestEncloser, sourceOfSynthesis, ok = splitQuery(closestEncloser)
        }
    }
    return ""
}

func keyExists(key string, z *Zone) bool {
    _, ok := z.Locations[key]
    return ok
}

func keyMatches(key string, z *Zone) bool {
    for value := range z.Locations {
        if strings.HasSuffix(value, key) {
            return true
        }
    }
    return false
}

func splitQuery(query string) (string, string, bool) {
    if query == "" {
        return "", "", false
    }
    var (
        splits            []string
        closestEncloser   string
        sourceOfSynthesis string
    )
    splits = strings.SplitAfterN(query, ".", 2)
    if len(splits) == 2 {
        closestEncloser = splits[1]
        sourceOfSynthesis = "*." + closestEncloser
    } else {
        closestEncloser = ""
        sourceOfSynthesis = "*"
    }
    return closestEncloser, sourceOfSynthesis, true
}

func split255(s string) []string {
    if len(s) < 255 {
        return []string{s}
    }
    sx := []string{}
    p, i := 0, 255
    for {
        if i <= len(s) {
            sx = append(sx, s[p:i])
        } else {
            sx = append(sx, s[p:])
            break

        }
        p, i = p+255, i+255
    }

    return sx
}

func (h *DnsRequestHandler) Matches(qname string) string {
    zone := ""
    h.zoneLock.RLock()
    zones := h.Zones
    h.zoneLock.RUnlock()
    for _, zname := range zones {
        if dns.IsSubDomain(zname, qname) {
            // We want the *longest* matching zone, otherwise we may end up in a parent
            if len(zname) > len(zone) {
                zone = zname
            }
        }
    }
    return zone
}

func (h *DnsRequestHandler) GetRecord(qname string) (record *dns_types.Record, rcode int) {
    // log.Printf("[DEBUG] GetRecord")

    // log.Println("[DEBUG]", h.Zones)
    if time.Since(h.LastZoneUpdate) > time.Duration(h.ZoneReload) * time.Second {
        // log.Printf("[DEBUG] loading zones")
        h.LoadZones()
    }
    // log.Println("[DEBUG]", h.Zones)

    zone := h.Matches(qname)
    // log.Printf("[DEBUG] zone : %s", zone)
    if zone == "" {
        log.Printf("[ERROR] no matching zone found for %s", qname)
        return nil, dns.RcodeNotAuth
    }

    z := h.LoadZone(zone)
    if z == nil {
        log.Printf("[ERROR] empty zone : %s", zone)
        return nil, dns.RcodeServerFailure
    }

    location := h.findLocation(qname, z)
    if len(location) == 0 { // empty, no results
        return nil, dns.RcodeNameError
    }
    // log.Printf("[DEBUG] location : %s", location)

    record = h.GetLocation(location, z)
    record.ZoneName = zone

    return record, dns.RcodeSuccess
}

func (h *DnsRequestHandler) LoadZone(zone string) *Zone {
    z := new(Zone)
    z.Name = zone
    vals := h.Redis.GetHKeys(zone)
    z.Locations = make(map[string]struct{})
    for _, val := range vals {
        z.Locations[val] = struct{}{}
    }

    return z
}

func (h *DnsRequestHandler) GetLocation(location string, z *Zone) *dns_types.Record {
    var label string
    if location == z.Name {
        label = "@"
    } else {
        label = location
    }
    val := h.Redis.HGet(z.Name, label)
    r := new(dns_types.Record)
    r.Config.IpFilterMode = "multi"
    r.Config.HealthCheckConfig.Enable = false
    err := json.Unmarshal([]byte(val), r)
    if err != nil {
        log.Printf("[ERROR] cannot parse json : %s -> %s", val, err)
        return nil
    }
    return r
}

func (h *DnsRequestHandler) SetLocation(location string, z *Zone, val *dns_types.Record) {
    jsonValue, err := json.Marshal(val)
    if err != nil {
        log.Printf("[ERROR] cannot encode to json : %s", err)
        return
    }
    var label string
    if location == z.Name {
        label = "@"
    } else {
        label = location
    }
    h.Redis.HSet(z.Name, label, string(jsonValue))
}

func GetWeightedIp(ips []dns_types.IP_Record) []dns_types.IP_Record {
    sum := 0
    for _, ip := range ips {
        sum += ip.Weight
    }
    x := rand.Intn(sum)
    index := 0
    for ; index < len(ips); index++ {
        x -= ips[index].Weight
        if x <= 0 {
            break
        }
    }
    return []dns_types.IP_Record { ips[index] }
}

func GetANAME(location string, proxy string, qtype uint16) []dns.RR {
    m := new(dns.Msg)
    m.SetQuestion(location, qtype)
    r, err := dns.Exchange(m, proxy)
    if err != nil {
        log.Printf("[ERROR] failed to retreive record from proxy %s : %s", proxy, err)
        return []dns.RR{}
    }
    if len(r.Answer) == 0 {
        return []dns.RR{}
    }
    return r.Answer
}

const (
    hostmaster = "hostmaster"
)
