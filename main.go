package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

var ctx = context.Background()

// Redis client
var rdb = redis.NewClient(&redis.Options{
	Addr: "localhost:6379",
})

// Konfigurasi
const (
	MaxPaymentSlot = 1000
	PaymentTTL     = 5 * time.Minute
)

// Data event statis (eventID -> total stock)
var events = map[string]int{
	"1001": 1,  // Event A punya 10 tiket
	"2002": 20,  // Event B punya 20 tiket
}

// Data user statis (anggap sudah login, userID berupa string)
var users = []string{"u1", "u2", "u3", "u4", "u5"}

func main() {
	// init stok tiket ke Redis saat start
	for eventID, stock := range events {
		key := fmt.Sprintf("tickets:stock:%s", eventID)
		rdb.Set(ctx, key, stock, 0)
	}

	r := gin.Default()
	r.POST("/buy/:eventID/:userID", buyTicketHandler)
	r.Run(":8000")
}

func buyTicketHandler(c *gin.Context) {
	eventID := c.Param("eventID")
	userID := c.Param("userID")

	// Validasi event
	if _, ok := events[eventID]; !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "event not found"})
		return
	}

	// Validasi user (cek ada di array users)
	if !userExists(userID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user not found"})
		return
	}

	// Redis keys
	stockKey := fmt.Sprintf("tickets:stock:%s", eventID)
	paymentKey := fmt.Sprintf("queue:payment:%s", eventID)
	waitingKey := fmt.Sprintf("queue:waiting:%s", eventID)
	userStatusKey := fmt.Sprintf("user:status:%s:%s", eventID, userID)

	// Cek stok
	stock, err := rdb.Get(ctx, stockKey).Int()
	if err == redis.Nil || stock <= 0 {
		// kalau habis → waiting list
		rdb.RPush(ctx, waitingKey, userID)
		rdb.Set(ctx, userStatusKey, "waiting", 0)
		c.JSON(http.StatusOK, gin.H{"status": "waiting"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "redis error"})
		return
	}

	// Hitung jumlah di payment queue
	paymentCount, err := rdb.ZCard(ctx, paymentKey).Result()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "redis error"})
		return
	}

	if paymentCount < MaxPaymentSlot {
		// masuk payment queue
		expireAt := time.Now().Add(PaymentTTL).Unix()

		pipe := rdb.TxPipeline()
		pipe.ZAdd(ctx, paymentKey, redis.Z{Score: float64(expireAt), Member: userID})
		pipe.Set(ctx, userStatusKey, "payment", PaymentTTL)
		pipe.Decr(ctx, stockKey)
		_, err := pipe.Exec(ctx)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "transaction failed"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"status":     "payment",
			"expire_at":  expireAt,
			"paymentTTL": PaymentTTL.Seconds(),
		})
	} else {
		// masuk waiting list
		pipe := rdb.TxPipeline()
		pipe.RPush(ctx, waitingKey, userID)
		pipe.Set(ctx, userStatusKey, "waiting", 0)
		_, err := pipe.Exec(ctx)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "transaction failed"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"status": "waiting"})
	}
}

// helper cek user ada/tidak
func userExists(userID string) bool {
	for _, u := range users {
		if u == userID {
			return true
		}
	}
	return false
}