package jrpc2

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// authorization describes user/password/network for HTTP Basic Authorization
type authorization struct {
	Username string
	Password string // bcrypt password hash or plain text

	Networks []*net.IPNet
}

// isRemoteNetworkAllowed validates network access for Remote Client IP
func isRemoteNetworkAllowed(networks []*net.IPNet, remoteIP net.IP) bool {
	// not valid remote IP
	if remoteIP == nil {
		return false
	}

	// empty network
	if networks == nil {
		return false
	}

	// check all allowed networks
	for _, network := range networks {
		if network.Contains(remoteIP) {
			return true
		}
	}

	return false
}

// CheckAuthorization checks Basic Authorization then enabled by service configuration.
func (s *Service) CheckAuthorization(r *http.Request) error {
	var remoteIP net.IP

	// authorize then auth disabled
	if s.auth == nil {
		return nil
	}

	// get remote client IP
	if s.behindReverseProxy {
		remoteIP = GetClientAddressFromHeader(r)
	} else {
		remoteIP = GetClientAddressFromRequest(r)
	}

	// get Basic Authorization data
	username, password, ok := r.BasicAuth()
	if !ok {
		return errors.New("empty Authorization header")
	}

	// lookup in ACL
	auth, ok := s.auth[username]
	if !ok {
		return errors.New("not authorized")
	}

	if !isRemoteNetworkAllowed(auth.Networks, remoteIP) {
		return errors.New("not authorized")
	}

	// check for bcrypt (golang native) encoded password
	if strings.HasPrefix(password, "$2a$") {
		if err := bcrypt.CompareHashAndPassword([]byte(auth.Password), []byte(password)); err != nil {
			return errors.New("not authorized")
		}

		return nil
	}

	// check for bcrypt (apache2 native) encoded password
	if strings.HasPrefix(password, "$2y$") {
		password = "$2a$" + strings.TrimPrefix(password, "$2y$")

		if err := bcrypt.CompareHashAndPassword([]byte(auth.Password), []byte(password)); err != nil {
			return errors.New("not authorized")
		}

		return nil
	}

	// password is plain text
	if password == auth.Password {
		return nil
	}

	// fallback on error
	return errors.New("not authorized")
}

// AddAuthorization adds (enables) Basic Authorization from specified remote network.
// When at least one mapping exists, Basic Authorization is enabled, default action is Deny Access.
// Сolon ':' is used as a delimiter, must not be in username or/and password.
// To generate hashed password record use (CPU intensive, use cost below 10): htpasswd -nbB username password
func (s *Service) AddAuthorization(username, password string, networks []string) error {
	// validate username input
	if strings.Contains(username, ":") {
		return fmt.Errorf("username '%s' must not contain ':'", username)
	}

	// validate password input
	if strings.Contains(password, ":") {
		return fmt.Errorf("password '%s' must not contain ':'", password)
	}

	// validate network input
	if len(networks) == 0 {
		return fmt.Errorf("network list must not be empty")
	}

	// networks as native object
	netsObj := make([]*net.IPNet, 0, len(networks))

	// process network lists
	for _, network := range networks {
		_, netObj, err := net.ParseCIDR(network)
		if err != nil {
			return fmt.Errorf("invalid network '%s': %v", network, err)
		}
		if netObj == nil {
			return fmt.Errorf("invalid network '%s'", network)
		}

		netsObj = append(netsObj, netObj)
	}

	// fail if user was already in map
	if _, ok := s.auth[username]; ok {
		return fmt.Errorf("user '%s' already added to authorization mapping", username)
	}

	// define map for the first rule
	if s.auth == nil {
		s.auth = make(map[string]authorization)
	}

	// add Authorization to Network mapping
	s.auth[username] = authorization{
		Username: username,
		Password: password,
		Networks: netsObj,
	}

	return nil
}

// AddAuthorizationFromFile adds (enables) Basic Authorization from file at path.
// When at least one mapping exists, Basic Authorization is enabled, default action is Deny Access.
// Сolon ':' is used as a delimiter, must not be in username or/and password.
// To generate hashed password record use (CPU intensive, use cost below 10): htpasswd -nbB username password
func (s *Service) AddAuthorizationFromFile(path string) error {

	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	entries := make([]*authorization, 0)

	scanner := bufio.NewScanner(file)

	for scanner.Scan() {

		// parse and fail on error
		auth, err := parseLine(scanner.Text())
		if err != nil {
			return err
		}

		// nil might return if line is a comment
		if auth == nil {
			continue
		}

		// fail if user was already added to authorization mapping
		if _, ok := s.auth[auth.Username]; ok {
			return fmt.Errorf("user '%s' already added to authorization mapping", auth.Username)
		}

		entries = append(entries, auth)

	}

	if err := scanner.Err(); err != nil {
		return err
	}

	// define map for the first rule if entries found in file
	if s.auth == nil && len(entries) > 0 {
		s.auth = make(map[string]authorization)
	}

	// add Authorizations to Network mapping
	for _, entry := range entries {
		s.auth[entry.Username] = *entry
	}

	return nil
}

// parseLine returns nil, nil if line is a comment (starts with #).
// In other cases either auth or error are returned.
func parseLine(line string) (*authorization, error) {

	const errpref = "parsing error:"

	line = strings.TrimSpace(line)

	// skip comments
	if strings.HasPrefix(line, "#") || line == "" {
		return nil, nil
	}

	// split line
	splitted := strings.Split(line, ":")

	// need minimum 3 entries
	if len(splitted) < 3 {
		return nil, fmt.Errorf("%s less than 3 items after splitting line '%s'", errpref, line)
	}

	// user and password are 1st and 2nd
	user := splitted[0]
	password := splitted[1]

	// networks can also have ":" - getting them by trimming user and password
	networksRaw := strings.TrimPrefix(line, fmt.Sprintf("%s:%s:", user, password))

	// error during trimming
	if networksRaw == line {
		return nil, fmt.Errorf("%s can't to trim user:password from line '%s'", errpref, line)
	}

	// networks are expected to be splitted by coma
	networks := strings.Split(networksRaw, ",")

	out := &authorization{
		Username: user,
		Password: password,
	}

	for _, n := range networks {

		n = strings.TrimSpace(n)

		if n == "" {
			continue
		}

		// parsing network
		_, parsed, err := net.ParseCIDR(n)
		if err != nil || parsed == nil {

			// error parsing even 1 network will fail whole line parsing
			return nil, fmt.Errorf("%s can't get network from line '%s'", errpref, line)
		}

		out.Networks = append(out.Networks, parsed)
	}

	// at least 1 network must be present
	if len(out.Networks) == 0 {
		return nil, fmt.Errorf("%s no networks found on line '%s'", errpref, line)
	}

	return out, nil
}
