package model

import (
	"../constant"
	"database/sql"
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"gopkg.in/redis.v3"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"strconv"
	"time"
)

/** random string **/
const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ123456789"
const (
	letterIdxBits = 6                    // 6 bits to represent a letter index
	letterIdxMask = 1<<letterIdxBits - 1 // All 1-bits, as many as letterIdxBits
	letterIdxMax  = 63 / letterIdxBits   // # of letter indices fitting in 63 bits
)

var src = rand.NewSource(time.Now().UnixNano())

func RandString(n int) string {
	b := make([]byte, n)
	// A src.Int63() generates 63 random bits, enough for letterIdxMax characters!
	for i, cache, remain := n-1, src.Int63(), letterIdxMax; i >= 0; {
		if remain == 0 {
			cache, remain = src.Int63(), letterIdxMax
		}
		if idx := int(cache & letterIdxMask); idx < len(letterBytes) {
			b[i] = letterBytes[idx]
			i--
		}
		cache >>= letterIdxBits
		remain--
	}

	return string(b)
}

/** random string **/

var L = log.New(os.Stderr, "", 0)
var r = redis.NewClient(&redis.Options{
	Addr:     os.Getenv("REDIS_HOST") + ":" + os.Getenv("REDIS_PORT"),
	Password: "",
	DB:       0,
})

type userType struct {
	id, name, password string
}

var cache_user = make(map[string]userType) //token -> UserType
var cache_userid = make(map[string]string) //name -> id
var cache_food_price = make(map[string]int)
var cache_food_stock = make(map[string]int)
var cache_token_user = make(map[string]string)
var cache_food_last_update_time int

func atoi(str string) int {
	res, err := strconv.Atoi(str)
	if err != nil {
		L.Panic(err)
	}
	return res
}

var addFood, queryStock, placeOrder *redis.Script

func Load_script_from_file(filename string) *redis.Script {
	command_raw, err := ioutil.ReadFile(filename)
	if err != nil {
		L.Fatal("Failed to load script " + filename)
	}
	command := string(command_raw)
	//return r.ScriptLoad(command).Val()
	return redis.NewScript(command)
}

func PostLogin(username string, password string) (int, string, string) {
	//fmt.Println("username=" + username)
	//fmt.Println("password=" + password)

	user_id, ok := cache_userid[username]
	if !ok {
		return -1, "", ""
	}

	password_ := cache_user[user_id].password
	if password != password_ {
		return -1, "", ""
	}

	token := RandString(8)
	//fmt.Println("token = " + token)
	s := fmt.Sprintf("token:%s:user", token)
	r.Set(s, user_id, 0)
	cache_token_user[token] = user_id
	return 0, user_id, token
}



func get_token_user(token string) string {
	if id, ok := cache_token_user[token]; ok {
		return id
	} else {
		s := fmt.Sprintf("token:%s:user", token)
		user_id := r.Get(s).Val()
		if user_id != "" {
			cache_token_user[token] = user_id
		}

		return user_id
	}
}

func Is_token_exist(token string) bool {
	if nid := get_token_user(token); nid == "" {
		return false
	} else {
		return true
	}
}

func Create_cart(token string) string {
	cartid := RandString( 32 )
	r.Set(fmt.Sprintf("cart:%s:user", cartid), get_token_user(token), 0)
	return cartid
}

func Cart_add_food(token, cartid string, foodid int, count int) int {
	foodid_s := strconv.Itoa(foodid)
	count_s := strconv.Itoa(count)
	num ,exist := cache_food_price[foodid_s]
	if !exist {
		L.Print(foodid, " has ", num)
		return -2
	}
	res, err := addFood.Run(
		r,
		[]string {token, cartid, foodid_s, count_s},
		[]string{}    ).Result()
		
	if err!=nil {
		L.Fatal(err)
	}
	
	return int(res.(int64))
}

func Get_foods() []map[string]interface{} {
	stock_delta := queryStock.Run(
		r,
		[]string { strconv.Itoa(cache_food_last_update_time) },
		[]string{}               ).Val().([]interface{})
	cache_food_last_update_time, _ = stock_delta[1].(int)
	for i := 2; i < len(stock_delta); i += 2 {
		id := stock_delta[i].(string)
		stock, _ := strconv.Atoi(stock_delta[i+1].(string))
		cache_food_stock[id] = stock
	}
	var ret []map[string]interface{}
	for k, _ := range cache_food_price {
		food_id, _ := strconv.Atoi(k)
		ret = append(ret, map[string]interface{}{
			"id":    food_id,
			"price": cache_food_price[k],
			"stock": cache_food_stock[k],
		})
	}
	return ret
}

func PostOrder(cart_id string, token string) (int, string) {
	order_id := RandString(8)
	res, err := placeOrder.Run(r, []string{cart_id, order_id, token}, []string{}).Result()
	if err!=nil {
		L.Fatal("Failed to post order, err:", err)
	}
	rtn := int(res.(int64))
	return rtn, order_id
}

func GetOrder(token string) (ret map[string]interface{}, found bool) {
	userid := get_token_user(token)
	uid, _ := strconv.Atoi(userid)
	orderid := r.Get(fmt.Sprintf("user:%s:order", get_token_user(token))).Val()
	if orderid == "" {
		found = false
		return
	}
	found = true
	cartid := r.HGet("order:cart", orderid).Val()
	items := r.HGetAll(fmt.Sprintf("cart:%s", cartid)).Val()
	var item_arr []map[string]int
	total := 0
	for i := 0; i < len(items); i += 2 {
		food := items[i]
		count := items[i+1]
		f, _ := strconv.Atoi(food)
		c, _ := strconv.Atoi(count)
		price := cache_food_price[food]
		total += price * c
		item_arr = append(item_arr, map[string]int{"food_id": f, "count": c})
	}
	ret = map[string]interface{}{
		"userid":  uid,
		"orderid": orderid,
		"items":   item_arr,
		"total":   total,
	}
	return
}

/** init code **/

func init_cache_and_redis(init_redis bool) {
	L.Print("Actual init begins, init_redis=", init_redis)
	addFood = Load_script_from_file("src/model/lua/add_food.lua")
	queryStock = Load_script_from_file("src/model/lua/query_stock.lua")
	placeOrder = Load_script_from_file("src/model/lua/place_order.lua")
	cache_food_last_update_time = 0
	db, dberr := sql.Open("mysql",
		os.Getenv("DB_USER")+
			":"+
			os.Getenv("DB_PASS")+
			"@tcp("+
			os.Getenv("DB_HOST")+
			":"+
			os.Getenv("DB_PORT")+
			")/"+
			os.Getenv("DB_NAME"))
	defer db.Close()
	if dberr != nil {
		L.Fatal(dberr)
	}
	
	if init_redis {
		r.FlushAll()
		r.ScriptFlush()
	}

	now := 0
	rows, _ := db.Query("SELECT id,name,password from user")
	for rows.Next() {
		var id, name, pwd string
		rows.Scan(&id, &name, &pwd)
		cache_userid[name] = id
		cache_user[id] =
			userType{
				id:       id,
				name:     name,
				password: pwd,
			}
	}
	
	rows, _ = db.Query("SELECT id,stock,price from food")
	p := r.Pipeline()
	for rows.Next() {
		var id string
		var stock, price int
		rows.Scan(&id, &stock, &price)
		idInt := atoi(id)
		now += 1
		cache_food_price[id] = price
		cache_food_stock[id] = stock
		L.Print("adding food:",id)
		if init_redis {
			p.ZAdd(constant.FOOD_STOCK_KIND,
				redis.Z{
					float64(now),
					now*constant.TIME_BASE + idInt,
				})
			p.ZAdd(constant.FOOD_STOCK_COUNT,
				redis.Z{
					float64(now),
					now*constant.TIME_BASE + stock,
				})
			p.HSet(constant.FOOD_LAST_UPDATE_TIME, id, strconv.Itoa(now))
		}
	}
	if init_redis {
		p.Set(constant.TIMESTAMP, now, 0)
		p.Set(constant.INIT_TIME, -10000, 0)
		p.Exec()
	}

}

func Sync_redis_from_mysql() {
	if constant.DEBUG {
		r.Del(constant.INIT_TIME)
	}

	if r.Incr(constant.INIT_TIME).Val() == 1 {
		L.Println("Ready to init redis")
		init_cache_and_redis(true)
	} else {
		L.Println("Already been init")
		init_cache_and_redis(false)
		for atoi(r.Get(constant.INIT_TIME).Val()) >= 1 {
			time.Sleep(200 * time.Millisecond)
		}
	}
}

/** init code **/