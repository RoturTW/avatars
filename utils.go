package main

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/nfnt/resize"
)

func roundCorners(imageData []byte, radius int) ([]byte, string, error) {
	cacheKey := fmt.Sprintf("%x-%d", md5.Sum(imageData), radius)

	cacheMutex.RLock()
	if cached, exists := roundedCache[cacheKey]; exists {
		if time.Since(cached.Timestamp) < time.Duration(cacheTimeout)*time.Second {
			cacheMutex.RUnlock()
			return cached.Data, cached.ContentType, nil
		}
	}
	cacheMutex.RUnlock()

	img, _, err := image.Decode(bytes.NewReader(imageData))
	if err != nil {
		return imageData, "image/jpeg", err
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	if radius > height/2 {
		radius = height / 2
	}

	result := image.NewRGBA(bounds)

	isInRoundedRect := func(x, y int) bool {
		topLeftCenterX := radius
		topLeftCenterY := radius
		topRightCenterX := width - radius - 1
		topRightCenterY := radius
		bottomLeftCenterX := radius
		bottomLeftCenterY := height - radius - 1
		bottomRightCenterX := width - radius - 1
		bottomRightCenterY := height - radius - 1

		if x < radius && y < radius {
			dx := x - topLeftCenterX
			dy := y - topLeftCenterY
			return dx*dx+dy*dy <= radius*radius
		} else if x >= width-radius && y < radius {
			dx := x - topRightCenterX
			dy := y - topRightCenterY
			return dx*dx+dy*dy <= radius*radius
		} else if x < radius && y >= height-radius {
			dx := x - bottomLeftCenterX
			dy := y - bottomLeftCenterY
			return dx*dx+dy*dy <= radius*radius
		} else if x >= width-radius && y >= height-radius {
			dx := x - bottomRightCenterX
			dy := y - bottomRightCenterY
			return dx*dx+dy*dy <= radius*radius
		}

		return true
	}

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if isInRoundedRect(x-bounds.Min.X, y-bounds.Min.Y) {
				result.Set(x, y, img.At(x, y))
			} else {
				result.Set(x, y, color.RGBA{0, 0, 0, 0})
			}
		}
	}

	var buf bytes.Buffer
	err = png.Encode(&buf, result)
	if err != nil {
		return imageData, "image/jpeg", err
	}

	resultData := buf.Bytes()

	cacheMutex.Lock()
	roundedCache[cacheKey] = CachedImage{
		Data:        resultData,
		ContentType: "image/png",
		Timestamp:   time.Now(),
	}
	cacheMutex.Unlock()

	return resultData, "image/png", nil
}

func resizeImage(imageData []byte, size int) ([]byte, error) {
	cacheKey := fmt.Sprintf("%x-%d", md5.Sum(imageData), size)

	cacheMutex.RLock()
	if cached, exists := resizedCache[cacheKey]; exists {
		if time.Since(cached.Timestamp) < time.Duration(cacheTimeout)*time.Second {
			cacheMutex.RUnlock()
			return cached.Data, nil
		}
	}
	cacheMutex.RUnlock()

	img, _, err := image.Decode(bytes.NewReader(imageData))
	if err != nil {
		return imageData, err
	}

	resized := resize.Resize(uint(size), uint(size), img, resize.Lanczos3)

	var buf bytes.Buffer
	err = jpeg.Encode(&buf, resized, &jpeg.Options{Quality: 85})
	if err != nil {
		return imageData, err
	}

	result := buf.Bytes()

	cacheMutex.Lock()
	resizedCache[cacheKey] = CachedImage{
		Data:        result,
		ContentType: "image/jpeg",
		Timestamp:   time.Now(),
	}
	cacheMutex.Unlock()

	return result, nil
}

func loadDefaultImage() {
	resp, err := http.Get(defaultImageURL)
	if err != nil {
		log.Printf("Error loading default image: %v", err)
		createFallbackImage()
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		defaultImageContent, err = io.ReadAll(resp.Body)
		if err != nil {
			log.Printf("Error reading default image: %v", err)
			createFallbackImage()
			return
		}
		defaultImageEtag = fmt.Sprintf("%x", md5.Sum(defaultImageContent))
	} else {
		createFallbackImage()
	}
}

func createFallbackImage() {
	img := image.NewRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 200, B: 200, A: 255})
		}
	}

	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85})
	defaultImageContent = buf.Bytes()
	defaultImageEtag = fmt.Sprintf("%x", md5.Sum(defaultImageContent))
}

func enableCORS() gin.HandlerFunc {
	config := cors.DefaultConfig()
	config.AllowAllOrigins = true
	config.AllowMethods = []string{"GET", "POST", "OPTIONS"}
	config.AllowHeaders = []string{"Content-Type"}
	return cors.New(config)
}

func toString(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case []any:
		var s string
		for _, item := range v {
			s += toString(item)
		}
		return s
	case map[string]any:
		var s string
		for k, v := range v {
			s += fmt.Sprintf("%s: %s\n", k, toString(v))
		}
		return s
	default:
		return fmt.Sprintf("%v", v)
	}
}

func getUserBy(username string) (*User, error) {
	usersFile, err := os.ReadFile("users.json")
	if err != nil {
		return nil, err
	}

	var users []User
	if err := json.Unmarshal(usersFile, &users); err != nil {
		return nil, err
	}

	var user *User
	for _, u := range users {
		if strings.EqualFold(u.Username, username) {
			user = &u
			break
		}
	}
	if user == nil {
		return nil, fmt.Errorf("user not found")
	}

	return user, nil
}
