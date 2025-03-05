package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/gin-gonic/gin"
)

const (
	MON_ADDRESS  = "0x0000000000000000000000000000000000000000"
	DAK_ADDRESS  = "0x0F0BDEbF0F83cD1EE3974779Bcb7315f9808c714"
	LBTC_ADDRESS = "0x73a58b73018c1a417534232529b57b99132b13D2"
	USDC_ADDRESS = "0xf817257fed379853cDe0fa4F97AB987181B1E5Ea"
	USDT_ADDRESS = "0xf817257fed379853cDe0fa4F97AB987181B1E5Ea" //cause route not found,we will use same price with usdc
	WETH_ADDRESS = "0xB5a30b0FDc5EA94A52fDc42e3E9760Cb8449Fb37"
	WBTC_ADDRESS = "0xcf5a6076cfa32686c0Df13aBaDa2b40dec133F1d"
	CACHE_TTL    = 2 * time.Hour
)

var tokenAddresses = map[string]string{
	"mon":  MON_ADDRESS,
	"wmon": MON_ADDRESS, //we will use same as mon token
	"dak":  DAK_ADDRESS,
	"lbtc": LBTC_ADDRESS,
	"usdc": USDC_ADDRESS,
	"usdt": USDT_ADDRESS,
	"eth":  WETH_ADDRESS,
	"wbtc": WBTC_ADDRESS,
}

type Result struct {
	Input struct {
		Amount float64 `json:"amount"`
		Token  string  `json:"token"`
	} `json:"input"`
	Output struct {
		Amount float64 `json:"amount"`
		Token  string  `json:"token"`
	} `json:"output"`
	ExchangeRate float64 `json:"exchange_rate"`
	Timestamp    string  `json:"timestamp"`
}

type CacheEntry struct {
	Result    Result
	ExpiresAt time.Time
}

type TokenPairCache struct {
	mutex sync.RWMutex
	cache map[string]map[string]map[string]CacheEntry
}

func NewTokenPairCache() *TokenPairCache {
	return &TokenPairCache{
		cache: make(map[string]map[string]map[string]CacheEntry),
	}
}

func (c *TokenPairCache) Get(inputToken, outputToken, amount string) (Result, bool) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	if _, ok := c.cache[inputToken]; !ok {
		return Result{}, false
	}

	if _, ok := c.cache[inputToken][outputToken]; !ok {
		return Result{}, false
	}

	entry, ok := c.cache[inputToken][outputToken][amount]
	if !ok {
		return Result{}, false
	}

	if time.Now().After(entry.ExpiresAt) {
		return Result{}, false
	}

	return entry.Result, true
}

func (c *TokenPairCache) Set(inputToken, outputToken, amount string, result Result) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if _, ok := c.cache[inputToken]; !ok {
		c.cache[inputToken] = make(map[string]map[string]CacheEntry)
	}

	if _, ok := c.cache[inputToken][outputToken]; !ok {
		c.cache[inputToken][outputToken] = make(map[string]CacheEntry)
	}

	c.cache[inputToken][outputToken][amount] = CacheEntry{
		Result:    result,
		ExpiresAt: time.Now().Add(CACHE_TTL),
	}
}

var cache = NewTokenPairCache()

func fetchTokenPrice(inputToken, outputToken, amount, targetURL string) (Result, error) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
	)

	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	ctx, cancel := chromedp.NewContext(allocCtx, chromedp.WithLogf(log.Printf))
	defer cancel()

	ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var inputValue, outputValue string

	err := chromedp.Run(ctx,
		chromedp.Navigate(targetURL),
		chromedp.WaitVisible(`input[data-sentry-element="Input"]`, chromedp.ByQuery),
		chromedp.Clear(`input[data-sentry-element="Input"]`, chromedp.ByQuery),
		chromedp.SendKeys(`input[data-sentry-element="Input"]`, amount, chromedp.ByQuery),
		chromedp.Sleep(5*time.Second),
		chromedp.Value(`input[data-sentry-element="Input"]`, &inputValue, chromedp.ByQuery),
		chromedp.Evaluate(`Array.from(document.querySelectorAll('input[data-sentry-element="Input"]')).filter(el => el.placeholder === "0.00")[1]?.value || "0"`, &outputValue),
		chromedp.ActionFunc(func(ctx context.Context) error {
			if outputValue == "0" || outputValue == "" {
				var result string
				err := chromedp.Evaluate(`document.querySelector('div[data-sentry-component="SwapInput"]:nth-of-type(2) input[data-sentry-element="Input"]').value`, &result).Do(ctx)
				if err == nil && result != "" {
					outputValue = result
				}
			}
			return nil
		}),
	)

	if err != nil {
		return Result{}, err
	}

	inputValue = strings.TrimSpace(inputValue)
	outputValue = strings.TrimSpace(outputValue)

	inputAmount, err := strconv.ParseFloat(inputValue, 64)
	if err != nil {
		return Result{}, err
	}

	outputAmount, err := strconv.ParseFloat(outputValue, 64)
	if err != nil {
		return Result{}, err
	}

	var decimalPlaces int
	switch outputToken {
	case "lbtc":
		decimalPlaces = 8
	case "usdc":
		decimalPlaces = 2
	case "usdt":
		decimalPlaces = 2
	case "eth":
		decimalPlaces = 5
	case "wbtc":
		decimalPlaces = 8
	default:
		decimalPlaces = 2
	}

	factor := math.Pow10(decimalPlaces)
	outputAmount = math.Floor(outputAmount*factor) / factor

	exchangeRate := outputAmount / inputAmount

	result := Result{
		Input: struct {
			Amount float64 `json:"amount"`
			Token  string  `json:"token"`
		}{
			Amount: inputAmount,
			Token:  inputToken,
		},
		Output: struct {
			Amount float64 `json:"amount"`
			Token  string  `json:"token"`
		}{
			Amount: outputAmount,
			Token:  outputToken,
		},
		ExchangeRate: exchangeRate,
		Timestamp:    time.Now().Format(time.RFC3339),
	}

	return result, nil
}

func handleTokenPrice(c *gin.Context) {
	startTime := time.Now()

	inputToken := c.Query("input")
	outputToken := c.Query("output")
	amount := c.Query("amount")

	if inputToken == "" || outputToken == "" || amount == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "input, output, and amount parameters are required"})
		return
	}

	var fromAddress, toAddress string
	var exists bool

	fromAddress, exists = tokenAddresses[inputToken]
	if !exists {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported input token: " + inputToken})
		return
	}

	toAddress, exists = tokenAddresses[outputToken]
	if !exists {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported output token: " + outputToken})
		return
	}

	if cachedResult, found := cache.Get(inputToken, outputToken, amount); found {
		duration := time.Since(startTime)
		log.Printf("[CACHE HIT] Request processed in %v", duration)
		c.JSON(http.StatusOK, cachedResult)
		return
	}

	targetURL := fmt.Sprintf("https://kuru.io/swap?from=%s&to=%s", fromAddress, toAddress)
	result, err := fetchTokenPrice(inputToken, outputToken, amount, targetURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	cache.Set(inputToken, outputToken, amount, result)

	duration := time.Since(startTime)
	log.Printf("[CACHE MISS] Request processed in %v", duration)

	c.JSON(http.StatusOK, result)
}

func setupRouter() *gin.Engine {
	router := gin.Default()
	router.GET("/", handleTokenPrice)
	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	return router
}

func main() {
	router := setupRouter()
	err := router.Run(":3000")
	if err != nil {
		log.Fatal("Failed to start server: ", err)
	}
}
