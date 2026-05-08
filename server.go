package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"os"
	"time"

	"github.com/lib/pq"
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

var basePrices = map[string]float64{
	"iPhone X": 13000, "iPhone XR": 13500, "iPhone XS": 13000, "iPhone XS Max": 16000,
	"iPhone 11": 15500, "iPhone 11 Pro": 19000, "iPhone 11 Pro Max": 23000,
	"iPhone 12 mini": 16000, "iPhone 12": 19500, "iPhone 12 Pro": 28000, "iPhone 12 Pro Max": 33000,
	"iPhone 13 mini": 21000, "iPhone 13": 28500, "iPhone 13 Pro": 37000, "iPhone 13 Pro Max": 43000,
	"iPhone 14": 35000, "iPhone 14 Plus": 35000, "iPhone 14 Pro": 46000, "iPhone 14 Pro Max": 50000,
	"iPhone 15": 45000, "iPhone 15 Plus": 45000, "iPhone 15 Pro": 56000, "iPhone 15 Pro Max": 65000,
	"iPhone 16": 60000, "iPhone 16 Plus": 60000, "iPhone 16 Pro": 75000, "iPhone 16 Pro Max": 90000,
	"iPhone 16e": 60000, "iPhone 17": 85000, "iPhone 17 Air": 85000, "iPhone 17 Pro": 105000, "iPhone 17 Pro Max": 110000,
	"iPhone 17e": 85000,
}

var regionCoeffs = map[string]float64{
	"Москва": 1.0, "Санкт-Петербург": 0.95,
	"Новосибирск": 0.88, "Екатеринбург": 0.88,
}

var colorCategories = map[string]float64{
	"Black": 1.0, "White": 1.0, "Space Gray": 1.0, "Silver": 1.0, "Midnight": 1.0,
}

var defectMultipliers = map[string]float64{
	"Замена дисплея (неоригинал)": 0.85,
	"Замена дисплея (оригинал)":   0.96,
	"True Tone не работает":       0.97,
	"Face ID не работает":         0.88,
	"Не работает верхний динамик": 0.97,
	"Не работает нижний динамик":  0.98,
	"Кнопка \"+\" не работает":    0.97,
	"Кнопка \"-\" не работает":    0.97,
	"Кнопка включения не работает": 0.96,
	"Замена аккумулятора (неоригинал)": 0.95,
	"Замена аккумулятора (оригинал)":   0.98,
	"Заднее стекло разбито":        0.92,
	"Трещина/скол на камере":      0.96,
	"Заблокированное устройство":   0.15,
}

var cosmeticGrades = map[string]float64{
	"excellent": 0.98,
	"good":      0.94,
	"fair":      0.85,
	"poor":      0.72,
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

	// Регион
	rawCoeff := regionCoeffs[req.Region]
	if rawCoeff == 0 {
		rawCoeff = 0.85 // по умолчанию
	}
	modelYear := getModelYear(req.Model)
	modelAge := 2026 - modelYear
	regionCoeff := rawCoeff
	if modelAge < 1 {
		regionCoeff = math.Min(rawCoeff+0.05, 1.0)
	}

	// SIM
	simCoeff := 1.0
	switch req.Sim {
	case "1 Sim":
		simCoeff = 0.95
	case "eSim":
		simCoeff = 0.95
	case "2 Sim":
		simCoeff = 1.05
	}

	// Память
	storageCoeff := 1.0
	switch req.Storage {
	case 128:
		storageCoeff = 1.08
	case 256:
		storageCoeff = 1.15
	case 512:
		storageCoeff = 1.15
	case 1024:
		storageCoeff = 1.15
	case 2048:
		storageCoeff = 1.15
	}

	// Цвет
	colorCoeff := colorCategories[req.Color]
	if colorCoeff == 0 {
		colorCoeff = 1.0
	}

	// Батарея
	batteryCoeff := 1.0
	health := float64(req.Battery)
	if health >= 80 {
		batteryCoeff = 1.0 - (100-health)*0.0025
	} else {
		batteryCoeff = 0.95 - (80-health)*(0.10/30)
	}

	// Внешний вид
	condCoeff := cosmeticGrades[req.Cosmetic]
	if condCoeff == 0 {
		condCoeff = 1.0
	}

	// Комплект
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

	// Минимальная цена
	minPrice := 7000.0
	if req.Model == "iPhone X" || req.Model == "iPhone XS" || req.Model == "iPhone XR" {
		minPrice = 4500
	}
	if market < minPrice {
		market = minPrice
	}

	// Маржа перекупа
	margin := 0.14
	if modelAge < 0.5 {
		margin = 0.06
	} else if modelAge < 1 {
		margin = 0.08
	} else if modelAge < 2 {
		margin = 0.11
	} else if modelAge < 3 {
		margin = 0.13
	}

	buyPrice := int(math.Floor(market*(1-margin)/500)) * 500
	sellPrice := int(math.Floor(market/500))*500 - 1

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

	var durationMin int
	var maxUses, currentUses int
	err := db.QueryRow("SELECT duration_min, max_uses, current_uses FROM promocodes WHERE code=$1", req.Code).Scan(&durationMin, &maxUses, &currentUses)
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

	expires := time.Now().Add(time.Duration(durationMin) * time.Minute)
	_, err = db.Exec("UPDATE promocodes SET current_uses = current_uses + 1 WHERE code=$1", req.Code)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ActivateResponse{Success: false, Error: "Ошибка сервера"})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ActivateResponse{Success: true, Expires: expires.Format(time.RFC3339)})
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
	case "iPhone X": return 2017
	case "iPhone XS", "iPhone XS Max", "iPhone XR": return 2018
	case "iPhone 11", "iPhone 11 Pro", "iPhone 11 Pro Max": return 2019
	case "iPhone 12", "iPhone 12 mini", "iPhone 12 Pro", "iPhone 12 Pro Max": return 2020
	case "iPhone 13", "iPhone 13 mini", "iPhone 13 Pro", "iPhone 13 Pro Max": return 2021
	case "iPhone 14", "iPhone 14 Plus", "iPhone 14 Pro", "iPhone 14 Pro Max": return 2022
	case "iPhone 15", "iPhone 15 Plus", "iPhone 15 Pro", "iPhone 15 Pro Max": return 2023
	case "iPhone 16", "iPhone 16 Plus", "iPhone 16 Pro", "iPhone 16 Pro Max", "iPhone 16e": return 2024
	case "iPhone 17", "iPhone 17 Air", "iPhone 17 Pro", "iPhone 17 Pro Max", "iPhone 17e": return 2025
	default: return 2025
	}
}