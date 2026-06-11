package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"os"
	"time"

	_ "github.com/lib/pq"
	"github.com/rs/cors"
)

var db *sql.DB

type CalculateRequest struct {
	Model    string          `json:"model"`
	Storage  int             `json:"storage"`
	Sim      string          `json:"sim"`
	Color    string          `json:"color"`
	Battery  int             `json:"battery"`
	Cosmetic string          `json:"cosmetic"`
	Defects  map[string]bool `json:"defects"`
	Kit      map[string]bool `json:"kit"`
	Region   string          `json:"region"`
}

type CalculateResponse struct {
	MarketPrice float64 `json:"marketPrice"`
	BuyPrice    int     `json:"buyPrice"`
	SellPrice   int     `json:"sellPrice"`
}

type ActivateRequest struct {
	Code     string `json:"code"`
	Username string `json:"username"`
}

type ActivateResponse struct {
	Success bool   `json:"success"`
	Expires string `json:"expires,omitempty"`
	Error   string `json:"error,omitempty"`
}

type AuthRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type AuthResponse struct {
	Success bool   `json:"success"`
	Expires string `json:"expires,omitempty"`
	Error   string `json:"error,omitempty"`
}

var basePrices = map[string]float64{
	"iPhone X": 12200, "iPhone XR": 12700, "iPhone XS": 12200, "iPhone XS Max": 15100,
	"iPhone 11": 14700, "iPhone 11 Pro": 18600, "iPhone 11 Pro Max": 22500,
	"iPhone 12 mini": 15200, "iPhone 12": 18600, "iPhone 12 Pro": 27400, "iPhone 12 Pro Max": 32300,
	"iPhone 13 mini": 19600, "iPhone 13": 27400, "iPhone 13 Pro": 36200, "iPhone 13 Pro Max": 42000,
	"iPhone 14": 34200, "iPhone 14 Plus": 34200, "iPhone 14 Pro": 44900, "iPhone 14 Pro Max": 48800,
	"iPhone 15": 43900, "iPhone 15 Plus": 43900, "iPhone 15 Pro": 54600, "iPhone 15 Pro Max": 63400,
	"iPhone 16": 58200, "iPhone 16 Plus": 58200, "iPhone 16 Pro": 72700, "iPhone 16 Pro Max": 87300,
	"iPhone 16e": 58200, "iPhone 17": 82000, "iPhone Air": 82000, "iPhone 17 Pro": 101000, "iPhone 17 Pro Max": 105500,
	"iPhone 17e": 82000,
}

var regionCoeffs = map[string]float64{
	"Москва": 1.0, "Санкт-Петербург": 0.95,
	"Новосибирск": 0.88, "Екатеринбург": 0.88,
}

var colorCategories = map[string]float64{
	"Black": 1.0, "White": 1.0, "Space Gray": 1.0, "Silver": 1.0, "Midnight": 1.0,
}

var defectMultipliers = map[string]float64{
	"Замена дисплея (неоригинал)":      0.85,
	"Замена дисплея (оригинал)":        0.96,
	"True Tone не работает":            0.97,
	"Face ID не работает":              0.88,
	"Не работает верхний динамик":      0.97,
	"Не работает нижний динамик":       0.98,
	"Кнопка \"+\" не работает":         0.97,
	"Кнопка \"-\" не работает":         0.97,
	"Кнопка включения не работает":     0.96,
	"Замена аккумулятора (неоригинал)": 0.95,
	"Замена аккумулятора (оригинал)":   0.98,
	"Заднее стекло разбито":            0.92,
	"Трещина/скол на камере":           0.96,
	"Заблокированное устройство":       0.15,
}

var cosmeticGrades = map[string]float64{
	"excellent": 0.98,
	"good":      0.94,
	"fair":      0.85,
	"poor":      0.72,
}

func hashPassword(password string) string {
	hash := sha256.Sum256([]byte(password))
	return hex.EncodeToString(hash[:])
}

func main() {
	var err error
	db, err = sql.Open("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/calculate", calculateHandler)
	mux.HandleFunc("/api/activate", activateHandler)
	mux.HandleFunc("/api/register", registerHandler)
	mux.HandleFunc("/api/login", loginHandler)
	mux.HandleFunc("/admin/promo", adminPromoHandler)

	handler := cors.Default().Handler(mux)
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	log.Fatal(http.ListenAndServe(":"+port, handler))
}

func calculateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req CalculateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	base, ok := basePrices[req.Model]
	if !ok {
		http.Error(w, "Unknown model", http.StatusNotFound)
		return
	}

	rawCoeff := regionCoeffs[req.Region]
	if rawCoeff == 0 {
		rawCoeff = 0.85
	}
	modelYear := getModelYear(req.Model)
	modelAge := 2026 - modelYear
	regionCoeff := rawCoeff
	if modelAge < 1 {
		regionCoeff = math.Min(rawCoeff+0.05, 1.0)
	}

	simCoeff := 1.0
	switch req.Sim {
	case "1 Sim":
		simCoeff = 0.95
	case "eSim":
		simCoeff = 0.95
	case "2 Sim":
		simCoeff = 1.05
	}

	storageCoeff := 1.0
	switch req.Storage {
	case 64:
		storageCoeff = 0.92
	case 128:
		storageCoeff = 1.0
	case 256:
		storageCoeff = 1.08
	case 512:
		storageCoeff = 1.22
	case 1024:
		storageCoeff = 1.35
	case 2048:
		storageCoeff = 1.55
	}

	colorCoeff := colorCategories[req.Color]
	if colorCoeff == 0 {
		colorCoeff = 1.0
	}

	batteryCoeff := 1.0
	health := float64(req.Battery)
	if health >= 80 {
		batteryCoeff = 1.0 - (100-health)*0.0025
	} else {
		batteryCoeff = 0.95 - (80-health)*(0.10/30)
	}

	condCoeff := cosmeticGrades[req.Cosmetic]
	if condCoeff == 0 {
		condCoeff = 1.0
	}

	kitCoeff := 0.90
	if req.Kit["box"] && req.Kit["cable"] {
		kitCoeff = 0.95
		if req.Kit["receipt"] {
			kitCoeff = 0.98
			if req.Kit["sticker"] {
				kitCoeff = 1.00
			}
		}
	}

	market := base * regionCoeff * simCoeff * storageCoeff * colorCoeff * condCoeff * batteryCoeff * kitCoeff

	for defect, active := range req.Defects {
		if !active {
			continue
		}
		if mult, exists := defectMultipliers[defect]; exists {
			market *= mult
		}
	}

	minPrice := 7000.0
	if req.Model == "iPhone X" || req.Model == "iPhone XS" || req.Model == "iPhone XR" {
		minPrice = 4500
	}
	if market < minPrice {
		market = minPrice
	}

	margin := 0.14
	if modelAge == 0 {
		margin = 0.06
	} else if modelAge == 1 {
		margin = 0.08
	} else if modelAge == 2 {
		margin = 0.11
	} else if modelAge == 3 {
		margin = 0.13
	}

	buyPrice := int(math.Floor(market*(1-margin)/500)) * 500
	sellPrice := int(math.Round(market/500)) * 500

	resp := CalculateResponse{
		MarketPrice: market,
		BuyPrice:    buyPrice,
		SellPrice:   sellPrice,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func activateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req ActivateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	var userID int
	err := db.QueryRow("SELECT id FROM users WHERE username=$1", req.Username).Scan(&userID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ActivateResponse{Success: false, Error: "Пользователь не найден"})
		return
	}

	var durationMin, maxUses, currentUses int
	err = db.QueryRow("SELECT duration_min, max_uses, current_uses FROM promocodes WHERE code=$1", req.Code).Scan(&durationMin, &maxUses, &currentUses)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ActivateResponse{Success: false, Error: "Неверный промокод"})
		return
	}
	if currentUses >= maxUses {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ActivateResponse{Success: false, Error: "Промокод исчерпан"})
		return
	}

	var used int
	err = db.QueryRow("SELECT COUNT(*) FROM promo_usages WHERE user_id=$1 AND code=$2", userID, req.Code).Scan(&used)
	if err == nil && used > 0 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ActivateResponse{Success: false, Error: "Вы уже использовали этот промокод"})
		return
	}

	expires := time.Now().Add(time.Duration(durationMin) * time.Minute)
	_, err = db.Exec("UPDATE users SET subscription_expires = $1 WHERE id = $2", expires, userID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ActivateResponse{Success: false, Error: "Ошибка сервера"})
		return
	}

	_, err = db.Exec("UPDATE promocodes SET current_uses = current_uses + 1 WHERE code=$1", req.Code)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ActivateResponse{Success: false, Error: "Ошибка сервера"})
		return
	}

	_, err = db.Exec("INSERT INTO promo_usages (user_id, code) VALUES ($1, $2)", userID, req.Code)
	if err != nil {
		log.Printf("Failed to insert promo usage: %v", err)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ActivateResponse{Success: true, Expires: expires.Format(time.RFC3339)})
}

func registerHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req AuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	var existing int
	err := db.QueryRow("SELECT id FROM users WHERE username=$1", req.Username).Scan(&existing)
	if err == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(AuthResponse{Success: false, Error: "Пользователь уже существует"})
		return
	}

	hashed := hashPassword(req.Password)
	_, err = db.Exec("INSERT INTO users (username, password_hash) VALUES ($1, $2)", req.Username, hashed)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(AuthResponse{Success: false, Error: "Ошибка регистрации"})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(AuthResponse{Success: true})
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req AuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	var storedHash string
	var expires sql.NullTime
	err := db.QueryRow("SELECT password_hash, subscription_expires FROM users WHERE username=$1", req.Username).Scan(&storedHash, &expires)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(AuthResponse{Success: false, Error: "Неверные данные"})
		return
	}

	hashed := hashPassword(req.Password)
	if storedHash != hashed {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(AuthResponse{Success: false, Error: "Неверные данные"})
		return
	}

	if expires.Valid && expires.Time.Before(time.Now()) {
		db.Exec("UPDATE users SET subscription_expires = NULL WHERE username=$1", req.Username)
	}

	resp := AuthResponse{Success: true}
	if expires.Valid && expires.Time.After(time.Now()) {
		resp.Expires = expires.Time.Format(time.RFC3339)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func adminPromoHandler(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Admin-Key") != os.Getenv("ADMIN_KEY") {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var promo struct {
		Code        string `json:"code"`
		DurationMin int    `json:"duration_min"`
		MaxUses     int    `json:"max_uses"`
	}
	if err := json.NewDecoder(r.Body).Decode(&promo); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	_, err := db.Exec("INSERT INTO promocodes (code, duration_min, max_uses) VALUES ($1,$2,$3)", promo.Code, promo.DurationMin, promo.MaxUses)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func getModelYear(model string) int {
	switch model {
	case "iPhone X":
		return 2017
	case "iPhone XS", "iPhone XS Max", "iPhone XR":
		return 2018
	case "iPhone 11", "iPhone 11 Pro", "iPhone 11 Pro Max":
		return 2019
	case "iPhone 12", "iPhone 12 mini", "iPhone 12 Pro", "iPhone 12 Pro Max":
		return 2020
	case "iPhone 13", "iPhone 13 mini", "iPhone 13 Pro", "iPhone 13 Pro Max":
		return 2021
	case "iPhone 14", "iPhone 14 Plus", "iPhone 14 Pro", "iPhone 14 Pro Max":
		return 2022
	case "iPhone 15", "iPhone 15 Plus", "iPhone 15 Pro", "iPhone 15 Pro Max":
		return 2023
	case "iPhone 16", "iPhone 16 Plus", "iPhone 16 Pro", "iPhone 16 Pro Max", "iPhone 16e":
		return 2024
	case "iPhone 17", "iPhone Air", "iPhone 17 Pro", "iPhone 17 Pro Max", "iPhone 17e":
		return 2025
	default:
		return 2025
	}
}
