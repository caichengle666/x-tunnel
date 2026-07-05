package main

import "flag"

var (
	// Routing rule strings (set by flags)
	directStr       string
	proxyStr        string
	defaultRouteStr string

	// Geo data file paths
	geoipFile   string
	geositeFile string
)

func init() {
	flag.StringVar(&directStr, "direct", "", "直连规则 (默认自动: geosite:cn,geoip:cn,private)")
	flag.StringVar(&proxyStr, "proxy", "", "代理规则 (逗号分隔)")
	flag.StringVar(&defaultRouteStr, "default", "proxy", "默认路由命中失败后: proxy 或 direct")
	flag.StringVar(&geoipFile, "geoip", "geoip.dat", "GeoIP 数据文件路径")
	flag.StringVar(&geositeFile, "geosite", "geosite.dat", "GeoSite 数据文件路径")
}

// initClientRouting loads geoip/geosite and initializes routing rules.
func initClientRouting() {
	if geoipFile != "" {
		loadGeoIP()
	}
	if geositeFile != "" {
		loadGeoSite()
	}
	initRules(directStr, proxyStr, defaultRouteStr)
}
