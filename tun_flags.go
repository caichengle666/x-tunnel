//go:build windows

package main

import "flag"

var (
	tunMode    bool
	tunName    string
	tunMTU     int
	tunAddress string
	tunDNS     string

	// Custom routing rules
	directStr       string
	proxyStr        string
	defaultRouteStr string

	// Geo data file paths
	geoipFile   string
	geositeFile string
)

func init() {
	flag.BoolVar(&tunMode, "tun", false, "启用 TUN 模式（仅 Windows）")
	flag.StringVar(&tunName, "tun-name", "xtun", "TUN 网卡名称")
	flag.IntVar(&tunMTU, "tun-mtu", 9000, "TUN 接口 MTU")
	flag.StringVar(&tunAddress, "tun-addr", "172.18.0.1/30", "TUN 接口地址（CIDR）")
	flag.StringVar(&tunDNS, "tun-dns", "172.18.0.1", "TUN DNS 地址（DNS 请求会在 TUN 内被劫持）")

	flag.StringVar(&directStr, "direct", "", "直连规则（默认：geosite:cn,geoip:cn,geosite:private,geoip:private）")
	flag.StringVar(&proxyStr, "proxy", "", "代理规则（逗号分隔，格式同 -direct）")
	flag.StringVar(&defaultRouteStr, "default", "proxy", "规则未命中时的默认路由：proxy 或 direct")

	flag.StringVar(&geoipFile, "geoip", "geoip.dat", "GeoIP 数据文件路径")
	flag.StringVar(&geositeFile, "geosite", "geosite.dat", "GeoSite 数据文件路径")
}
