
package main

import (
	"log"
	"net/netip"
	"strings"
)

// RuleType identifies what kind of matching a rule performs.
type RuleType int

const (
	RuleTypeGeoSite RuleType = iota // geosite:xx
	RuleTypeGeoIP                   // geoip:xx
	RuleTypeDomain                  // domain:example.com
	RuleTypeCIDR                    // 1.2.3.0/24 or 2400:cb00::/32
)

// RouteRule is one entry from -direct or -proxy.
type RouteRule struct {
	Type  RuleType
	Value string // original string for logging

	// Parsed data (populated after init)
	geoSiteCat string       // geosite category, e.g. "cn"
	geoIPCat   string       // geoip country code, e.g. "cn"
	domain     string       // exact domain or suffix for domain: rule
	cidr       netip.Prefix // parsed CIDR for CIDR rules
}

// RouteDecision is the result of route matching.
type RouteDecision int

const (
	DecisionDirect RouteDecision = iota
	DecisionProxy
	DecisionNone // no rule matched
)

func (d RouteDecision) String() string {
	switch d {
	case DecisionDirect:
		return "direct"
	case DecisionProxy:
		return "proxy"
	default:
		return "none"
	}
}

var (
	directRules          []RouteRule
	proxyRules           []RouteRule
	defaultRouteDecision RouteDecision // fallback when no rule matches
)

// parseRuleList parses a comma-separated rule string into RouteRule slice.
// Supported formats:
//
//	geosite:cn        -> RuleTypeGeoSite
//	geoip:cn          -> RuleTypeGeoIP
//	geoip:private     -> RuleTypeGeoIP (special: private ranges)
//	geosite:private   -> RuleTypeGeoSite
//	domain:example.com -> RuleTypeDomain (exact or suffix match)
//	1.2.3.0/24        -> RuleTypeCIDR
//	2400:cb00::/32    -> RuleTypeCIDR
//	1.2.3.4           -> RuleTypeCIDR (single IP, auto /32 or /128)
func parseRuleList(s string) []RouteRule {
	if s == "" {
		return nil
	}
	var rules []RouteRule
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lower := strings.ToLower(part)

		if strings.HasPrefix(lower, "geosite:") {
			cat := part[strings.Index(part, ":")+1:]
			rules = append(rules, RouteRule{
				Type:       RuleTypeGeoSite,
				Value:      part,
				geoSiteCat: strings.ToLower(cat),
			})
		} else if strings.HasPrefix(lower, "geosite.") {
			// Alias: geosite.cn -> geosite:cn
			cat := part[strings.Index(part, ".")+1:]
			canonical := "geosite:" + strings.ToLower(cat)
			rules = append(rules, RouteRule{
				Type:       RuleTypeGeoSite,
				Value:      canonical,
				geoSiteCat: strings.ToLower(cat),
			})
		} else if strings.HasPrefix(lower, "geoip:") {
			cat := part[strings.Index(part, ":")+1:]
			rules = append(rules, RouteRule{
				Type:     RuleTypeGeoIP,
				Value:    part,
				geoIPCat: strings.ToLower(cat),
			})
		} else if strings.HasPrefix(lower, "geoip.") {
			// Alias: geoip.cn -> geoip:cn
			cat := part[strings.Index(part, ".")+1:]
			canonical := "geoip:" + strings.ToLower(cat)
			rules = append(rules, RouteRule{
				Type:     RuleTypeGeoIP,
				Value:    canonical,
				geoIPCat: strings.ToLower(cat),
			})
		} else if strings.HasPrefix(lower, "domain:") {
			d := part[strings.Index(part, ":")+1:]
			rules = append(rules, RouteRule{
				Type:   RuleTypeDomain,
				Value:  part,
				domain: strings.ToLower(d),
			})
		} else if strings.Contains(part, "/") {
			pfx, err := netip.ParsePrefix(part)
			if err != nil {
				log.Printf("[TUN] invalid CIDR rule: %s (%v)", part, err)
				continue
			}
			rules = append(rules, RouteRule{
				Type:  RuleTypeCIDR,
				Value: part,
				cidr:  pfx,
			})
		} else {
			// Try parsing as single IP -> auto-convert to /32 or /128
			addr, err := netip.ParseAddr(part)
			if err != nil {
				log.Printf("[TUN] invalid rule: %s (%v)", part, err)
				continue
			}
			bits := 32
			if addr.Is6() {
				bits = 128
			}
			rules = append(rules, RouteRule{
				Type:  RuleTypeCIDR,
				Value: part,
				cidr:  netip.PrefixFrom(addr, bits),
			})
		}
	}
	return rules
}

// matchDomain checks if a domain matches the given rule.
func matchDomain(rule RouteRule, domain string) bool {
	switch rule.Type {
	case RuleTypeGeoSite:
		matcher := getGeoSiteMatcher(rule.geoSiteCat)
		return matcher != nil && matcher.Match(domain)
	case RuleTypeDomain:
		d := strings.ToLower(domain)
		r := rule.domain
		return d == r || strings.HasSuffix(d, "."+r)
	}
	return false
}

// matchIP checks if an IP matches the given rule.
func matchIP(rule RouteRule, ip netip.Addr) bool {
	switch rule.Type {
	case RuleTypeGeoIP:
		matcher := getGeoIPMatcher(rule.geoIPCat)
		return matcher != nil && matcher.Contains(ip)
	case RuleTypeCIDR:
		return rule.cidr.Contains(ip)
	}
	return false
}

// routeDomain decides direct/proxy for a domain.
// Priority: direct rules (in order) > proxy rules (in order).
// Returns DecisionNone when no rule matches; callers apply defaultRouteDecision.
func routeDomain(domain string) (RouteDecision, string) {
	for _, r := range directRules {
		if matchDomain(r, domain) {
			return DecisionDirect, r.Value
		}
	}
	for _, r := range proxyRules {
		if matchDomain(r, domain) {
			return DecisionProxy, r.Value
		}
	}
	return DecisionNone, ""
}

// routeIP decides direct/proxy for an IP.
// Priority: direct rules (in order) > proxy rules (in order).
// Returns DecisionNone when no rule matches; callers apply defaultRouteDecision.
func routeIP(ip netip.Addr) (RouteDecision, string) {
	for _, r := range directRules {
		if matchIP(r, ip) {
			return DecisionDirect, r.Value
		}
	}
	for _, r := range proxyRules {
		if matchIP(r, ip) {
			return DecisionProxy, r.Value
		}
	}
	return DecisionNone, ""
}

// routeIPStr decides direct/proxy for an IP string.
func routeIPStr(ipStr string) (RouteDecision, string) {
	ip, err := netip.ParseAddr(ipStr)
	if err != nil {
		return DecisionNone, ""
	}
	return routeIP(ip)
}

// routeDomainOrIP first tries domain-based rules, then IP-based.
// This is the main entry point for TCP/UDP routing decisions.
// Returns (decision, matchedRule, isDomainMatch).
func routeDomainOrIP(domain string, ipStr string) (RouteDecision, string, bool) {
	// 1. Try domain rules first (if we have a domain from SNI sniffing)
	if domain != "" {
		if d, rule := routeDomain(domain); d != DecisionNone {
			return d, rule, true
		}
	}
	// 2. Try IP rules
	if d, rule := routeIPStr(ipStr); d != DecisionNone {
		return d, rule, false
	}
	// 3. Default
	return defaultRouteDecision, "", false // uses -default flag (proxy or direct)
}

// dnsRouteForDomain decides which DNS resolver to use for a domain.
// If any direct rule matches the domain -> local DNS (direct).
// Otherwise -> remote DNS (proxy).
func dnsRouteForDomain(domain string) (RouteDecision, string) {
	for _, r := range directRules {
		if matchDomain(r, domain) {
			return DecisionDirect, r.Value
		}
	}
	for _, r := range proxyRules {
		if matchDomain(r, domain) {
			return DecisionProxy, r.Value
		}
	}
	// Default: use -default flag
	if defaultRouteDecision == DecisionDirect {
		return DecisionDirect, "default"
	}
	return DecisionProxy, "default"
}

// shouldDirectTCP decides if a TCP connection should go direct.
// Returns (shouldDirect, reason).
func shouldDirectTCP(domain string, targetHost string) (bool, string) {
	decision, reason := routeTCP(domain, targetHost)
	return decision == DecisionDirect, reason
}

// routeTCP decides TCP route and returns a normalized log reason.
func routeTCP(domain string, targetHost string) (RouteDecision, string) {
	decision, rule, isDomain := routeDomainOrIP(domain, targetHost)
	if rule != "" {
		if isDomain {
			return decision, "domain:" + rule
		}
		return decision, "ip:" + rule
	}
	return decision, "default"
}

// shouldDirectUDP decides if a UDP packet should go direct.
func shouldDirectUDP(ipStr string) (bool, string) {
	decision, rule := routeIPStr(ipStr)
	if decision == DecisionNone {
		decision = defaultRouteDecision
		if decision == DecisionDirect {
			return true, "default"
		}
	}
	return decision == DecisionDirect, rule
}

// initRules initializes the routing rules from the parsed flags.
// This must be called after geoip.dat and geosite.dat are loaded.
func initRules(directStr, proxyStr, defaultStr string) {
	directRules = parseRuleList(directStr)
	proxyRules = parseRuleList(proxyStr)

	// Apply defaults when no rules specified by user.
	// User can override: -direct "..." for custom direct, -proxy "0.0.0.0/0,::/0" for global proxy.
	if len(directRules) == 0 && len(proxyRules) == 0 {
		directRules = []RouteRule{
			{Type: RuleTypeGeoSite, Value: "geosite:cn", geoSiteCat: "cn"},
			{Type: RuleTypeGeoIP, Value: "geoip:cn", geoIPCat: "cn"},
			{Type: RuleTypeGeoSite, Value: "geosite:private", geoSiteCat: "private"},
			{Type: RuleTypeGeoIP, Value: "geoip:private", geoIPCat: "private"},
		}
	}

	// Parse default route
	switch strings.ToLower(strings.TrimSpace(defaultStr)) {
	case "direct":
		defaultRouteDecision = DecisionDirect
	case "proxy":
		defaultRouteDecision = DecisionProxy
	default:
		log.Printf("[TUN] invalid -default value %q, using proxy", defaultStr)
		defaultRouteDecision = DecisionProxy
	}

	log.Printf("[TUN] direct rules: %s", ruleListSummary(directRules))
	log.Printf("[TUN] proxy rules: %s", ruleListSummary(proxyRules))
	log.Printf("[TUN] default route: %s", defaultRouteDecision)
}

func ruleListSummary(rules []RouteRule) string {
	if len(rules) == 0 {
		return "(none)"
	}
	var vals []string
	for _, r := range rules {
		vals = append(vals, r.Value)
	}
	return strings.Join(vals, ", ")
}
