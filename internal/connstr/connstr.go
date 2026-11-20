// Package connstr splits a connection string, a JDBC URL or an ssh target
// such as user@host, into the components that are secrets of their own: the
// host, the port, the account, and the password.
package connstr

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
)

// Part is one component of a connection string. Span is a byte range in
// the string that was split.
type Part struct {
	Kind finding.Kind // KindHost, KindPort, KindAccount, KindPassword
	Span finding.Span
}

// placeholder starts a ${...} property placeholder, which is not a value.
const placeholder = "${"

// Split returns the components of value, in order of position, and whether
// it knows value as a connection string. It returns nil and false when it
// does not know value. It returns no components and true when it knows value
// and every component in it is a ${...} placeholder, so nothing in it is a
// value.
//
// It knows three forms:
//
//   - jdbc:oracle:thin: or jdbc:oracle:oci: followed by an optional
//     account/password, then @ and host:port:SID, host:port/service, or
//     //host:port/service;
//   - any other jdbc:<subprotocol>://[account[:password]@]host[:port][,...]
//     with an optional path, and parameters after ? (separated by &) or ;
//     (separated by ;), as MySQL, PostgreSQL, MariaDB, and SQL Server write
//     them. The value of user or username is an account, and the value of
//     any parameter whose name contains password, pwd, secret, or token
//     (ignoring case) is a password. A value with an @ after the authority
//     is not split, because its user information may hold an unencoded
//     delimiter;
//   - account@host, account@host:port, and account@host:path, the target of
//     ssh and scp.
//
// A host may be an IPv6 address in brackets; the brackets are not part of
// it. A SQL Server instance name (host\instance) is not part of the host.
// The database name, the path, and other parameters are not components.
// A component that is a ${...} placeholder is left out, and every span
// starts and ends at an ASCII delimiter or an end of value, so it falls on
// character boundaries. A value that is not valid UTF-8, or whose host or
// port is malformed, is not known.
func Split(value []byte) ([]Part, bool) {
	if !utf8.Valid(value) {
		return nil, false
	}
	s := string(value)
	sp := &splitter{s: s}
	var ok bool
	switch {
	case strings.HasPrefix(s, "jdbc:oracle:"):
		ok = sp.oracle()
	case strings.HasPrefix(s, "jdbc:"):
		ok = sp.hierarchical()
	default:
		ok = sp.target()
	}
	if !ok {
		return nil, false
	}
	if len(sp.parts) == 0 {
		// Nothing is a component unless a placeholder stood where one would
		// be, as in a TNS descriptor, whose hosts are not split.
		return nil, sp.placeholders > 0
	}
	slices.SortFunc(sp.parts, func(a, b Part) int { return a.Span.Start - b.Span.Start })
	return sp.parts, true
}

// IsPlaceholder reports whether value is one ${...} placeholder: it starts
// with ${ and ends with the } that closes it.
func IsPlaceholder(value []byte) bool {
	s := string(value)
	return strings.HasPrefix(s, placeholder) && closeBrace(s, len(placeholder)) == len(s)-1
}

// splitter collects the parts of s.
type splitter struct {
	s     string
	parts []Part
	// placeholders counts the components left out because they are
	// placeholders.
	placeholders int
}

// add records s[start:end] as a part of kind k, unless it is empty or a
// placeholder.
func (sp *splitter) add(k finding.Kind, start, end int) {
	if end <= start {
		return
	}
	if strings.HasPrefix(sp.s[start:end], placeholder) {
		sp.placeholders++
		return
	}
	sp.parts = append(sp.parts, Part{Kind: k, Span: finding.Span{Start: start, End: end}})
}

// oracle splits jdbc:oracle:<driver>:[account[/password]]@<connect>.
func (sp *splitter) oracle() bool {
	s := sp.s
	from := len("jdbc:oracle:")
	driverEnd := strings.IndexByte(s[from:], ':')
	if driverEnd < 0 {
		return false
	}
	if d := s[from : from+driverEnd]; d != "thin" && d != "oci" {
		return false
	}
	from += driverEnd + 1
	at := strings.LastIndexByte(s[from:], '@')
	if at < 0 {
		return false
	}
	credEnd := from + at
	if slash := index(s, from, credEnd, "/"); slash >= 0 {
		sp.add(finding.KindAccount, from, slash)
		sp.add(finding.KindPassword, slash+1, credEnd)
	} else {
		sp.add(finding.KindAccount, from, credEnd)
	}
	conn := credEnd + 1
	if strings.HasPrefix(s[conn:], "//") {
		conn += len("//")
	}
	if strings.HasPrefix(s[conn:], "(") {
		// A TNS descriptor, (DESCRIPTION=...), whose hosts are not split.
		return true
	}
	_, ok := sp.hostPort(conn, len(s), "/:?")
	return ok
}

// hierarchical splits jdbc:<subprotocol>://<authority><rest>.
func (sp *splitter) hierarchical() bool {
	s := sp.s
	sep := strings.Index(s, "://")
	if sep <= len("jdbc:") || strings.ContainsFunc(s[len("jdbc:"):sep], func(r rune) bool {
		return !isAlnum(r) && !strings.ContainsRune(":+.-", r)
	}) {
		return false
	}
	authStart := sep + len("://")
	authEnd := index(s, authStart, len(s), "/?;#")
	if authEnd < 0 {
		authEnd = len(s)
	}
	if strings.IndexByte(s[authEnd:], '@') >= 0 {
		// An @ after the end of the authority means the user information
		// holds an unencoded /, ?, ; or #, so where it ends is not known.
		return false
	}
	hosts := authStart
	if at := strings.LastIndexByte(s[authStart:authEnd], '@'); at >= 0 {
		infoEnd := authStart + at
		if colon := index(s, authStart, infoEnd, ":"); colon >= 0 {
			sp.add(finding.KindAccount, authStart, colon)
			sp.add(finding.KindPassword, colon+1, infoEnd)
		} else {
			sp.add(finding.KindAccount, authStart, infoEnd)
		}
		hosts = infoEnd + 1
	}
	for {
		end, ok := sp.hostPort(hosts, authEnd, ",")
		if !ok {
			return false
		}
		if end == authEnd {
			break
		}
		hosts = end + 1
	}
	sp.params(authEnd)
	return true
}

// params adds the account and secret parameters that follow the first ? or
// ; at or after from.
func (sp *splitter) params(from int) {
	s := sp.s
	start := index(s, from, len(s), "?;")
	if start < 0 {
		return
	}
	for i := start + 1; i < len(s); {
		end := index(s, i, len(s), "&;")
		if end < 0 {
			end = len(s)
		}
		eq := index(s, i, end, "=")
		if eq >= 0 {
			key, vStart, vEnd := strings.ToLower(s[i:eq]), eq+1, end
			// SQL Server allows a value in braces, which may hold ; and &.
			if strings.HasPrefix(s[vStart:], "{") {
				if closing := strings.IndexByte(s[vStart:], '}'); closing >= 0 {
					vStart, vEnd = vStart+1, vStart+closing
					end = index(s, vEnd, len(s), "&;")
					if end < 0 {
						end = len(s)
					}
				}
			}
			switch {
			case key == "user" || key == "username":
				sp.add(finding.KindAccount, vStart, vEnd)
			case secretKey(key):
				sp.add(finding.KindPassword, vStart, vEnd)
			}
		}
		i = end + 1
	}
}

// secretKey reports whether a parameter named key, in lower case, holds a
// secret: its name contains password, pwd, secret, or token, as
// password, sslpassword, trustCertificateKeyStorePassword, accessToken,
// and keyStoreSecret do.
func secretKey(key string) bool {
	for _, w := range []string{"password", "pwd", "secret", "token"} {
		if strings.Contains(key, w) {
			return true
		}
	}
	return false
}

// target splits account@host, account@host:port, and account@host:path.
func (sp *splitter) target() bool {
	s := sp.s
	at := strings.IndexByte(s, '@')
	if at <= 0 || !validAccount(s[:at]) {
		return false
	}
	hostEnd := index(s, at+1, len(s), ":/")
	if hostEnd < 0 {
		hostEnd = len(s)
	}
	if !validHost(s[at+1 : hostEnd]) {
		return false
	}
	sp.add(finding.KindAccount, 0, at)
	sp.add(finding.KindHost, at+1, hostEnd)
	if hostEnd < len(s) && s[hostEnd] == ':' && validPort(s[hostEnd+1:]) {
		sp.add(finding.KindPort, hostEnd+1, len(s))
	}
	return true
}

// hostPort adds the host and port of host[:port] that starts at from and
// ends at the first byte of stops, or at to. A host is ended by ':' or a
// byte of stops, and a port by a byte of stops. It returns the end and
// false when the host or the port is malformed.
func (sp *splitter) hostPort(from, to int, stops string) (int, bool) {
	s := sp.s
	hostStart, hostEnd, next := from, 0, 0
	if strings.HasPrefix(s[from:to], "[") {
		closing := strings.IndexByte(s[from:to], ']')
		if closing < 0 || !validIPv6(s[from+1:from+closing]) {
			return 0, false
		}
		hostStart, hostEnd, next = from+1, from+closing, from+closing+1
		if next < to && s[next] != ':' && !strings.ContainsRune(stops, rune(s[next])) {
			return 0, false
		}
	} else {
		next = index(s, from, to, ":"+stops)
		if next < 0 {
			next = to
		}
		hostEnd = next
		if inst := index(s, from, next, `\`); inst >= 0 {
			hostEnd = inst
		}
		if !validHost(s[hostStart:hostEnd]) {
			return 0, false
		}
	}
	sp.add(finding.KindHost, hostStart, hostEnd)
	if next >= to || s[next] != ':' {
		return next, true
	}
	portEnd := index(s, next+1, to, stops)
	if portEnd < 0 {
		portEnd = to
	}
	if !validPort(s[next+1 : portEnd]) {
		return 0, false
	}
	sp.add(finding.KindPort, next+1, portEnd)
	return portEnd, true
}

// index returns the index of the first byte of s[from:to] that is in
// chars, skipping ${...} placeholders, or -1 when there is none. An
// unclosed placeholder runs to the end.
func index(s string, from, to int, chars string) int {
	for i := from; i < to; i++ {
		if strings.HasPrefix(s[i:to], placeholder) {
			closing := closeBrace(s[:to], i+len(placeholder))
			if closing < 0 {
				return -1
			}
			i = closing
			continue
		}
		if strings.IndexByte(chars, s[i]) >= 0 {
			return i
		}
	}
	return -1
}

// closeBrace returns the index of the } that closes a { opened just before
// from, counting nested braces, or -1.
func closeBrace(s string, from int) int {
	depth := 1
	for i := from; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

func validHost(h string) bool {
	if strings.HasPrefix(h, placeholder) {
		return true
	}
	return h != "" && !strings.ContainsFunc(h, func(r rune) bool {
		return !isAlnum(r) && r != '.' && r != '-' && r != '_'
	})
}

func validIPv6(h string) bool {
	return h != "" && !strings.ContainsFunc(h, func(r rune) bool {
		return !isHex(r) && r != ':' && r != '.' && r != '%'
	})
}

func validPort(p string) bool {
	if strings.HasPrefix(p, placeholder) {
		return true
	}
	return p != "" && !strings.ContainsFunc(p, func(r rune) bool { return r < '0' || r > '9' })
}

func validAccount(a string) bool {
	return a != "" && !strings.ContainsFunc(a, func(r rune) bool {
		return !isAlnum(r) && r != '.' && r != '-' && r != '_'
	})
}

func isAlnum(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

func isHex(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F'
}
