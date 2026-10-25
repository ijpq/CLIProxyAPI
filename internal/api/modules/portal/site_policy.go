package portal

import (
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	siteLegacy     = "legacy"
	siteCustomer   = "customer"
	sitePrivileged = "privileged"
	siteUnknown    = "unknown"
)

var portalHostPattern = regexp.MustCompile("^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?([.][a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$")

// SitePolicy separates customer and explicitly approved existing accounts.
// Reverse proxies must overwrite Host with the configured site name.
// Forwarded host headers never select an account access boundary.
type SitePolicy struct {
	publicHost     string
	privilegedHost string
	privilegedIDs  map[string]struct{}
}

func NewSitePolicy(publicHost, privilegedHost, privilegedIDs string) (*SitePolicy, error) {
	publicHost = strings.ToLower(strings.TrimSpace(publicHost))
	privilegedHost = strings.ToLower(strings.TrimSpace(privilegedHost))
	privilegedIDs = strings.TrimSpace(privilegedIDs)
	if publicHost == "" && privilegedHost == "" && privilegedIDs == "" {
		return nil, nil
	}
	validHost := func(host string) bool { return len(host) <= 253 && portalHostPattern.MatchString(host) }
	if !validHost(publicHost) || !validHost(privilegedHost) || publicHost == privilegedHost || privilegedIDs == "" {
		return nil, errors.New("portal: configure distinct public/private hostnames and explicitly approved user IDs")
	}
	p := &SitePolicy{publicHost: publicHost, privilegedHost: privilegedHost, privilegedIDs: make(map[string]struct{})}
	for _, raw := range strings.Split(privilegedIDs, ",") {
		id, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil || id == uuid.Nil {
			return nil, errors.New("portal: privileged user IDs must be nonempty approved UUIDs")
		}
		p.privilegedIDs[id.String()] = struct{}{}
	}
	return p, nil
}

// SetSitePolicy must be called before the server starts accepting requests.
func (m *Module) SetSitePolicy(policy *SitePolicy) { m.sitePolicy = policy }

func (m *Module) siteMode(c *gin.Context) string {
	if m.sitePolicy == nil {
		return siteLegacy
	}
	host := strings.ToLower(c.Request.Host)
	if hostname, _, err := net.SplitHostPort(host); err == nil {
		host = hostname
	}
	host = strings.TrimSuffix(host, ".")
	switch host {
	case m.sitePolicy.publicHost:
		return siteCustomer
	case m.sitePolicy.privilegedHost:
		return sitePrivileged
	default:
		return siteUnknown
	}
}

func (m *Module) siteMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if m.siteMode(c) == siteUnknown {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.Next()
	}
}

func (m *Module) privateAdminSite() gin.HandlerFunc {
	return func(c *gin.Context) {
		if mode := m.siteMode(c); mode != siteLegacy && mode != sitePrivileged {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.Next()
	}
}

// canViewUpstreamQuota is evaluated only after authentication has resolved the
// current account. Site membership is independent of billing/admin flags.
func (m *Module) canViewUpstreamQuota(c *gin.Context) bool {
	if m.sitePolicy == nil {
		// Without an explicit privileged allowlist, only administrators qualify.
		return isAdminFromGin(c)
	}
	return m.siteMode(c) == sitePrivileged && m.siteAllowsUser(c, userIDFromGin(c))
}

func (m *Module) upstreamQuotaOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !m.canViewUpstreamQuota(c) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "上游额度仅供特权账户查看"})
			return
		}
		c.Next()
	}
}

func (m *Module) registrationAllowed(c *gin.Context) bool {
	mode := m.siteMode(c)
	return mode == siteLegacy || mode == siteCustomer
}

func (m *Module) siteAllowsUser(c *gin.Context, id string) bool {
	if m.sitePolicy == nil {
		return true
	}
	_, privileged := m.sitePolicy.privilegedIDs[id]
	switch m.siteMode(c) {
	case sitePrivileged:
		return privileged
	case siteCustomer:
		return !privileged
	default:
		return false
	}
}

func (m *Module) enforceUserSite(c *gin.Context, id string) bool {
	if m.siteAllowsUser(c, id) {
		return true
	}
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "请使用该账户对应的登录入口"})
	return false
}

func (m *Module) loginCodeKey(c *gin.Context, email string) string {
	if m.sitePolicy == nil {
		return email
	}
	return m.siteMode(c) + ":" + email
}
