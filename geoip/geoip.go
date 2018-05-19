package geoip

import (
    "math"
    "net"
    "log"

    "github.com/oschwald/maxminddb-golang"
    "arvancloud/redins/dns_types"
    "arvancloud/redins/eventlog"
    "arvancloud/redins/config"
)

type GeoIp struct {
    Enable bool
    db     *maxminddb.Reader
}

func NewGeoIp(config *config.RedinsConfig) *GeoIp {
    g := &GeoIp {
        Enable: config.GeoIp.Enable,
    }
    var err error
    if g.Enable {
        g.db, err = maxminddb.Open(config.GeoIp.Db)
        if err != nil {
            log.Printf("[ERROR] cannot open maxminddb file %s", err)
            g.Enable = false
            return g
        }
    }
    // defer g.db.Close()
    return g
}

func (g *GeoIp) GetSameCountry(sourceIp net.IP, ips []dns_types.IP_Record, logData *eventlog.RequestLogData) []dns_types.IP_Record {
    if g.Enable == false {
        return ips
    }
    _, _, sourceCountry, err := g.GetGeoLocation(sourceIp)
    if err != nil {
        log.Printf("[ERROR] getSameCountry failed")
        return ips
    }

    if len(ips) > 0 {
        matched := false
        matchedIndex := 0
        defaultIndex := 0
        for i, ip := range ips {
            if ip.Country == sourceCountry {
                matched = true
                matchedIndex = i
                break
            }
            if ip.Country == "" {
                defaultIndex = i
            }
        }
        if matched {
            logData.SourceCountry = sourceCountry
            logData.DestinationIp = ips[matchedIndex].Ip.String()
            logData.DestinationCountry = ips[matchedIndex].Country
            return []dns_types.IP_Record{ips[matchedIndex]}
        } else {
            logData.SourceCountry = sourceCountry
            logData.DestinationIp = ips[defaultIndex].Ip.String()
            logData.DestinationCountry = ips[defaultIndex].Country
            return []dns_types.IP_Record{ips[defaultIndex]}
        }
    }
    return []dns_types.IP_Record{}
}

func (g *GeoIp) GetMinimumDistance(sourceIp net.IP, ips []dns_types.IP_Record, logData *eventlog.RequestLogData) []dns_types.IP_Record {
    if g.Enable == false {
        return ips
    }
    minDistance := 1000.0
    index := -1
    slat, slong, _, err := g.GetGeoLocation(sourceIp)
    if err != nil {
        log.Printf("[ERROR] getMinimumDistance failed")
        return ips
    }
    for i, ip := range ips {
        destinationIp := ip.Ip
        dlat, dlong, _, err := g.GetGeoLocation(destinationIp)
        d, err := g.getDistance(slat, slong, dlat, dlong)
        if err != nil {
            continue
        }
        if d < minDistance {
            minDistance = d
            index = i
        }
    }
    if index > -1 {
        logData.SourceCountry = ""
        logData.DestinationIp = ips[index].Ip.String()
        logData.DestinationCountry = ips[index].Country
        return []dns_types.IP_Record { ips[index] }
    } else {
        log.Printf("[ERROR] getMinimumDistance failed")
        return ips
    }
}

func (g *GeoIp) getDistance(slat, slong, dlat, dlong float64) (float64, error) {
    deltaLat := (dlat - slat) * math.Pi / 180.0
    deltaLong := (dlong - slong) * math.Pi / 180.0
    slat = slat * math.Pi / 180.0
    dlat = dlat * math.Pi / 180.0

    a := math.Sin(deltaLat/2.0)*math.Sin(deltaLat/2.0) +
        math.Cos(slat)*math.Cos(dlat)* math.Sin(deltaLong/2.0)*math.Sin(deltaLong/2.0)
    c := 2.0 * math.Atan2(math.Sqrt(a), math.Sqrt(1.0-a))

    // log.Printf("[DEBUG] distance = ", c)

    return c, nil
}

func (g *GeoIp) GetGeoLocation(ip net.IP) (latitude float64, longitude float64, country string, err error) {
    if g.Enable == false {
        return
    }
    var record struct {
        Location struct {
            Latitude        float64 `maxminddb:"latitude"`
            LongitudeOffset uintptr `maxminddb:"longitude"`
        } `maxminddb:"location"`
        Country struct {
            ISOCode string `maxminddb:"iso_code"`
        } `maxminddb:"country"`
    }
    // log.Printf("[DEBUG], ip : %s\n", ip)
    err = g.db.Lookup(ip, &record)
    if err != nil {
        log.Printf("[ERROR] lookup failed : %s", err)
        return 0, 0, "", err
    }
    g.db.Decode(record.Location.LongitudeOffset, &longitude)
    // log.Printf("[DEBUG] lat = ", record.Location.Latitude, " lang = ", longitude)
    return record.Location.Latitude, longitude, record.Country.ISOCode, nil
}
