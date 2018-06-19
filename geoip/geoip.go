package geoip

import (
    "math"
    "net"

    "github.com/oschwald/maxminddb-golang"
    "arvancloud/redins/dns_types"
    "arvancloud/redins/config"
    "arvancloud/redins/eventlog"
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
            eventlog.Logger.Errorf("cannot open maxminddb file %s", err)
            g.Enable = false
            return g
        }
    }
    // defer g.db.Close()
    return g
}

func (g *GeoIp) GetSameCountry(sourceIp net.IP, ips []dns_types.IP_Record, logData map[string]interface{}) []dns_types.IP_Record {
    if g.Enable == false {
        return ips
    }
    _, _, sourceCountry, err := g.GetGeoLocation(sourceIp)
    if err != nil {
        eventlog.Logger.Error("getSameCountry failed")
        return ips
    }
    logData["SourceCountry"] = sourceCountry

    result := []dns_types.IP_Record{}
    for _, ip := range ips {
        if ip.Country == sourceCountry {
            result = append(result, ip)
        }
    }
    if len(result) > 0 {
        return result
    }

    for _, ip := range ips {
        if ip.Country == "" {
            result = append(result, ip)
        }
    }
    if len(result) > 0 {
        return result
    }

    return ips
}

func (g *GeoIp) GetMinimumDistance(sourceIp net.IP, ips []dns_types.IP_Record, logData map[string]interface{}) []dns_types.IP_Record {
    if g.Enable == false {
        return ips
    }
    minDistance := 1000.0
    dists := []float64{}
    result := []dns_types.IP_Record{}
    slat, slong, _, err := g.GetGeoLocation(sourceIp)
    if err != nil {
        eventlog.Logger.Error("getMinimumDistance failed")
        return ips
    }
    for _, ip := range ips {
        destinationIp := ip.Ip
        dlat, dlong, _, err := g.GetGeoLocation(destinationIp)
        d, err := g.getDistance(slat, slong, dlat, dlong)
        if err != nil {
            d = 1000.0
        }
        if d < minDistance {
            minDistance = d
        }
        dists = append(dists, d)
    }
    for i, ip := range ips {
        if dists[i] == minDistance {
            result = append(result, ip)
        }
    }
    if len(result) > 0 {
        return result
    }
    return ips
}

func (g *GeoIp) getDistance(slat, slong, dlat, dlong float64) (float64, error) {
    deltaLat := (dlat - slat) * math.Pi / 180.0
    deltaLong := (dlong - slong) * math.Pi / 180.0
    slat = slat * math.Pi / 180.0
    dlat = dlat * math.Pi / 180.0

    a := math.Sin(deltaLat/2.0)*math.Sin(deltaLat/2.0) +
        math.Cos(slat)*math.Cos(dlat)* math.Sin(deltaLong/2.0)*math.Sin(deltaLong/2.0)
    c := 2.0 * math.Atan2(math.Sqrt(a), math.Sqrt(1.0-a))

    eventlog.Logger.Debugf("distance = ", c)

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
    eventlog.Logger.Debugf("ip : %s", ip)
    err = g.db.Lookup(ip, &record)
    if err != nil {
        eventlog.Logger.Errorf("lookup failed : %s", err)
        return 0, 0, "", err
    }
    g.db.Decode(record.Location.LongitudeOffset, &longitude)
    eventlog.Logger.Debug("lat = ", record.Location.Latitude, " lang = ", longitude, " country = ", record.Country.ISOCode)
    return record.Location.Latitude, longitude, record.Country.ISOCode, nil
}
