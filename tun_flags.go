//go:build windows

package main

import "flag"

var (
	tunMode    bool
	tunName    string
	tunMTU     int
	tunAddress string
	tunDNS     string
)

func init() {
	flag.BoolVar(&tunMode, "tun", false, "启用 TUN 模式（仅 Windows）")
	flag.StringVar(&tunName, "tun-name", "xtun", "TUN 网卡名称")
	flag.IntVar(&tunMTU, "tun-mtu", 9000, "TUN 接口 MTU")
	flag.StringVar(&tunAddress, "tun-addr", "172.18.0.1/30", "TUN 接口地址（CIDR）")
	flag.StringVar(&tunDNS, "tun-dns", "172.18.0.1", "TUN DNS 地址（DNS 请求会在 TUN 内被劫持）")
}
