package migration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const providerResponseMaxBytes = 256 * 1024

// EstimatePrice performs bounded, read-only provider lookups. It is separate
// from the central availability endpoint because GoAsterWeb deliberately does
// not call or return pricing data.
func EstimatePrice(ctx context.Context, request PriceRequest, options Options) (PriceEstimate, error) {
	if err := validateMachineRequest(request.ServerType, request.Locations); err != nil {
		return PriceEstimate{}, err
	}
	options = completeOptions(options)
	token, err := options.LoadHetznerToken()
	if err != nil {
		return PriceEstimate{}, fmt.Errorf("load migration provider token: %w", err)
	}
	if !validHetznerToken(token) {
		return PriceEstimate{}, errors.New("migration provider token is invalid")
	}

	serverType, err := findServerType(ctx, options, token, request.ServerType)
	if err != nil {
		return PriceEstimate{}, err
	}
	pricing, err := fetchPricing(ctx, options, token)
	if err != nil {
		return PriceEstimate{}, err
	}

	allow := make(map[string]struct{}, len(request.Locations))
	for _, location := range request.Locations {
		allow[location] = struct{}{}
	}
	serverPrices := make(map[string]string, len(serverType.Prices))
	for _, price := range serverType.Prices {
		if !safeMachineName(price.Location, 128) {
			return PriceEstimate{}, errors.New("provider price response is malformed")
		}
		if _, exists := serverPrices[price.Location]; exists {
			return PriceEstimate{}, errors.New("provider price response contains a duplicate location")
		}
		if _, err := parseDecimal(price.PriceHourly.Gross); err != nil {
			return PriceEstimate{}, errors.New("provider price response is malformed")
		}
		serverPrices[price.Location] = price.PriceHourly.Gross
	}
	available := make(map[string]bool, len(serverType.Locations))
	for _, location := range serverType.Locations {
		if !safeMachineName(location.Name, 128) {
			return PriceEstimate{}, errors.New("provider server-type response is malformed")
		}
		if _, exists := available[location.Name]; exists {
			return PriceEstimate{}, errors.New("provider server-type response contains a duplicate location")
		}
		available[location.Name] = location.Available
	}

	names := make([]string, 0, len(available))
	for name, isAvailable := range available {
		if !isAvailable {
			continue
		}
		if len(allow) > 0 {
			if _, ok := allow[name]; !ok {
				continue
			}
		}
		names = append(names, name)
	}
	sort.Strings(names)
	result := PriceEstimate{ServerType: request.ServerType, Currency: pricing.Currency}
	for _, name := range names {
		serverGross, ok := serverPrices[name]
		if !ok {
			return PriceEstimate{}, errors.New("provider price response is missing a location")
		}
		ipv4Gross, ok := pricing.IPv4Prices[name]
		if !ok {
			return PriceEstimate{}, errors.New("provider pricing response is missing public IPv4 pricing")
		}
		serverValue, _ := parseDecimal(serverGross)
		ipv4Value, _ := parseDecimal(ipv4Gross)
		total := addDecimal(serverValue, ipv4Value)
		result.Locations = append(result.Locations, PriceLocation{Location: name, HourlyGross: decimalString(total)})
	}
	if len(result.Locations) == 0 {
		result.Status = "unavailable"
		return result, nil
	}
	result.Status = "quoted"
	return result, nil
}

// Quote is a concise compatibility name for EstimatePrice.
func Quote(ctx context.Context, request QuoteRequest, options Options) (QuoteResult, error) {
	return EstimatePrice(ctx, request, options)
}

type providerServerType struct {
	ID           int64                 `json:"id"`
	Name         string                `json:"name"`
	Architecture string                `json:"architecture"`
	Deprecated   bool                  `json:"deprecated"`
	Locations    []providerLocation    `json:"locations"`
	Prices       []providerServerPrice `json:"prices"`
}

type providerLocation struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
}

type providerServerPrice struct {
	Location    string `json:"location"`
	PriceHourly struct {
		Gross string `json:"gross"`
	} `json:"price_hourly"`
}

type providerServerTypesResponse struct {
	ServerTypes []providerServerType `json:"server_types"`
	Meta        struct {
		Pagination struct {
			LastPage int `json:"last_page"`
		} `json:"pagination"`
	} `json:"meta"`
}

type providerPricing struct {
	Currency   string
	IPv4Prices map[string]string
}

type providerPricingResponse struct {
	Pricing struct {
		Currency   string                  `json:"currency"`
		PrimaryIPs []providerPrimaryIPCost `json:"primary_ips"`
	} `json:"pricing"`
}

type providerPrimaryIPCost struct {
	Type   string                `json:"type"`
	Prices []providerServerPrice `json:"prices"`
}

func findServerType(ctx context.Context, options Options, token, name string) (providerServerType, error) {
	for page := 1; page <= 100; page++ {
		body, err := providerGET(ctx, options, token, "/server_types?per_page=100&page="+strconv.Itoa(page))
		if err != nil {
			return providerServerType{}, err
		}
		var response providerServerTypesResponse
		if err := json.Unmarshal(body, &response); err != nil {
			clear(body)
			return providerServerType{}, fmt.Errorf("decode provider server types: %w", err)
		}
		clear(body)
		for _, value := range response.ServerTypes {
			if value.Name == name {
				if value.Deprecated || value.Name == "" {
					return providerServerType{}, errors.New("requested server type is unavailable")
				}
				return value, nil
			}
		}
		if response.Meta.Pagination.LastPage <= page || len(response.ServerTypes) == 0 {
			break
		}
	}
	return providerServerType{}, errors.New("requested server type was not found")
}

func fetchPricing(ctx context.Context, options Options, token string) (providerPricing, error) {
	body, err := providerGET(ctx, options, token, "/pricing")
	if err != nil {
		return providerPricing{}, err
	}
	var response providerPricingResponse
	if err := json.Unmarshal(body, &response); err != nil {
		clear(body)
		return providerPricing{}, fmt.Errorf("decode provider pricing: %w", err)
	}
	clear(body)
	if len(response.Pricing.Currency) != 3 || strings.ToUpper(response.Pricing.Currency) != response.Pricing.Currency {
		return providerPricing{}, errors.New("provider pricing currency is malformed")
	}
	result := providerPricing{Currency: response.Pricing.Currency, IPv4Prices: make(map[string]string)}
	for _, item := range response.Pricing.PrimaryIPs {
		if item.Type != "ipv4" {
			continue
		}
		for _, price := range item.Prices {
			if !safeMachineName(price.Location, 128) {
				return providerPricing{}, errors.New("provider IPv4 pricing is malformed")
			}
			if _, exists := result.IPv4Prices[price.Location]; exists {
				return providerPricing{}, errors.New("provider IPv4 pricing contains a duplicate location")
			}
			if _, err := parseDecimal(price.PriceHourly.Gross); err != nil {
				return providerPricing{}, errors.New("provider IPv4 pricing is malformed")
			}
			result.IPv4Prices[price.Location] = price.PriceHourly.Gross
		}
	}
	return result, nil
}

func providerGET(ctx context.Context, options Options, token, path string) ([]byte, error) {
	endpoint, err := providerEndpoint(options.ProviderEndpoint, path, options.AllowInsecureHTTP)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build provider request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Del("Origin")
	response, err := options.HTTPClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("provider transport: %w", err)
	}
	if response == nil || response.Body == nil {
		return nil, errors.New("provider response body is unavailable")
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, providerResponseMaxBytes+1))
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		clear(body)
		return nil, fmt.Errorf("read provider response: %w", err)
	}
	if len(body) > providerResponseMaxBytes {
		clear(body)
		return nil, errors.New("provider response exceeds the limit")
	}
	if response.StatusCode != http.StatusOK {
		clear(body)
		return nil, fmt.Errorf("provider returned HTTP %d", response.StatusCode)
	}
	return body, nil
}

func providerEndpoint(origin, path string, allowInsecure bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSuffix(origin, "/"))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.ForceQuery || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("provider endpoint is invalid")
	}
	if parsed.Scheme != "https" && !(allowInsecure && parsed.Scheme == "http") {
		return "", errors.New("provider endpoint must use HTTPS")
	}
	pathURL, err := url.Parse(path)
	if err != nil || pathURL.User != nil || pathURL.ForceQuery || pathURL.Fragment != "" || !strings.HasPrefix(pathURL.Path, "/") {
		return "", errors.New("provider request path is invalid")
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/") + pathURL.Path
	parsed.RawPath = ""
	parsed.RawQuery = pathURL.RawQuery
	return parsed.String(), nil
}
