package util

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt"
	"github.com/google/uuid"
	"io"
	"log"
	"main/model"
	"main/response"
	"net/http"
	"os"
	"strings"
	"time"
)

const TableName = "Account"

var AlreadyExists = errors.New("account with this type already exists")
var InsufficientFounds = errors.New("insufficient funds")
var InvalidAccount = errors.New("invalid account")
var OpenAccount = errors.New("account is not closed")

var AccountTypesLimit = map[string]int{
	"checking": 50,
	"saving":   10,
}

func IsValidUUID(u string) bool {
	_, err := uuid.Parse(u)
	return err == nil
}

func GetPK(id string) string {
	if strings.Contains(id, "USER#") {
		return id
	}
	return "USER#" + id
}

func GetSK(id string) string {
	if strings.Contains(id, "ACCOUNT#") {
		return id
	}
	return "ACCOUNT#" + id
}

func upload(url, token string, payload []byte) {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(payload))
	if err != nil {
		log.Printf("NewRequest error: %v", err)
		return
	}
	req.Header.Add("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := http.Client{
		Timeout: 10 * time.Second,
	}

	res, err := client.Do(req)
	if err != nil {
		log.Printf("Do error: %v", err)
		return
	}

	data, err := io.ReadAll(res.Body)
	if err != nil {
		log.Printf("ReadAll error: %v", err)
		return
	}
	defer func(body io.ReadCloser) {
		if err := body.Close(); err != nil {
			log.Printf("Close error: %s\n", err)
		}
	}(res.Body)

	if res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusInternalServerError {
		log.Printf("ReadAll error: %v", string(data))
	}
}

func UploadStat(ctx *gin.Context) {
	payload, err := json.Marshal(map[string]string{"endpoint": ctx.FullPath()})
	if err != nil {
		log.Printf("Marshal error: %v", err)
		return
	}

	upload("http://account-stat:8090/api/v1/stat", ctx.MustGet("token").(string), payload)
}

func UploadAccount(account model.Account, ctx *gin.Context) {
	payload, err := json.Marshal(map[string]string{"userID": account.PK, "accountID": account.SK, "type": account.Type,
		"openDate": time.Now().Format("2006-01-02 15:04:05")})
	if err != nil {
		log.Printf("Marshal error: %v", err)
		return
	}

	upload("http://account-stat:8090/api/v1/account", ctx.MustGet("token").(string), payload)
}

func GetTransactions(accountID, token, correlation string) ([]model.Transaction, error) {
	req, err := http.NewRequest(http.MethodGet, "http://transaction-api:8085/api/v1/transaction/"+accountID+"/all", nil)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Authorization", "Bearer "+token)
	req.Header.Add("Correlation", correlation)

	client := http.Client{
		Timeout: 10 * time.Second,
	}

	res, err := client.Do(req)
	if err != nil {
		return []model.Transaction{}, err
	}

	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	defer func(body io.ReadCloser) {
		if err := body.Close(); err != nil {
			log.Printf("Close error: %s\n", err)
		}
	}(res.Body)

	if res.StatusCode == http.StatusNoContent {
		return []model.Transaction{}, nil
	}

	if res.StatusCode != http.StatusOK {
		return nil, errors.New("error: " + string(data))
	}

	var tr []model.Transaction
	if err := json.Unmarshal(data, &tr); err != nil {
		return nil, err
	}
	return tr, nil
}

func ValidateToken(context *gin.Context) {
	token := context.GetHeader("Authorization")
	if token == "" {
		context.JSON(http.StatusUnauthorized, response.ErrorResponse{Error: "unauthorized"})
		context.Abort()
		return
	}

	values := strings.Split(token, "Bearer ")
	if len(values) != 2 {
		context.JSON(http.StatusUnauthorized, response.ErrorResponse{Error: "token is not set properly"})
		context.Abort()
		return
	}
	token = values[1]

	to, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(os.Getenv("JWT_SECRET")), nil
	})
	if err != nil {
		context.JSON(http.StatusBadRequest, response.ErrorResponse{Error: err.Error()})
		context.Abort()
		return
	}

	if !to.Valid {
		context.JSON(http.StatusBadRequest, response.ErrorResponse{Error: "invalid token"})
		context.Abort()
		return
	}

	if claims, ok := to.Claims.(jwt.MapClaims); ok {
		if claims["sub"] == "" {
			context.JSON(http.StatusBadRequest, response.ErrorResponse{Error: "invalid id"})
			context.Abort()
			return
		}

		if claims["iat"] == "" || claims["exp"] == "" {
			context.JSON(http.StatusBadRequest, response.ErrorResponse{Error: "iat or exp not set"})
			context.Abort()
			return
		}

		tokenIat := time.Unix(int64(claims["iat"].(float64)), 0)
		if tokenIat.After(time.Now()) {
			context.JSON(http.StatusBadRequest, response.ErrorResponse{Error: "iat can't be in the future"})
			context.Abort()
			return
		}

		tokenExp := time.Unix(int64(claims["exp"].(float64)), 0)
		if tokenExp.Before(time.Now()) {
			context.JSON(http.StatusBadRequest, response.ErrorResponse{Error: "expired token"})
			context.Abort()
			return
		}

		context.Set("ID", claims["sub"])
		context.Set("token", token)
		context.Next()
		return
	}
	context.JSON(http.StatusBadRequest, response.ErrorResponse{Error: "invalid token"})
}

// RandomToken godoc
//
//	@Description	Get a random token.
//	@Summary		Get a random token.
//	@Produce		json
//	@Tags			auth
//	@Success		200	{object}	[]model.Token	"Token"
//	@Failure		500	{object}	response.ErrorResponse
//	@Router			/login [GET]
func RandomToken(context *gin.Context) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS512,
		jwt.MapClaims{
			"sub": uuid.New().String(),
			"iat": time.Now().Unix(),
			"exp": time.Now().Add(time.Hour * 24).Unix(),
		},
	)

	s, err := token.SignedString([]byte(os.Getenv("JWT_SECRET")))
	if err != nil {
		context.JSON(http.StatusInternalServerError, response.ErrorResponse{Error: err.Error()})
		return
	}
	context.JSON(http.StatusOK, model.Token{Token: s})
}

func CORS(context *gin.Context) {
	context.Header("Access-Control-Allow-Origin", "*")
	context.Header("Access-Control-Allow-Credentials", "true")
	context.Header("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, Authorization, Origin, Accept, Cache-Control")
	context.Header("Access-Control-Allow-Methods", "OPTIONS, POST, GET, PATCH, DELETE")
	context.Header("Access-Control-Max-Age", "86400")

	if context.Request.Method == http.MethodOptions {
		context.AbortWithStatus(http.StatusOK)
		return
	}
	context.Next()
}
