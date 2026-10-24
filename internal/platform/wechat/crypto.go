package wechat

import (
	"crypto/aes"
	"crypto/cipher"
	//nolint:gosec // WeChat specification mandates SHA-1 for user data signature verification.
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"

	"bgin/internal/platform/errs"
)

// Decrypt 使用 SessionKey 与 IV 解密微信小程序敏感数据（AES-128-CBC + PKCS#7）。
//
// 输入均为 Base64 编码的字符串，成功解密后返回原始 JSON 字节切片。
func Decrypt(sessionKey, encryptedData, iv string) ([]byte, error) {
	keyBytes, err := base64.StdEncoding.DecodeString(sessionKey)
	if err != nil {
		return nil, errs.BadRequest("wechat crypto: invalid base64 session_key")
	}
	if len(keyBytes) != aes.BlockSize {
		return nil, errs.BadRequest(fmt.Sprintf("wechat crypto: invalid session_key length, expected %d bytes", aes.BlockSize))
	}

	ivBytes, err := base64.StdEncoding.DecodeString(iv)
	if err != nil {
		return nil, errs.BadRequest("wechat crypto: invalid base64 iv")
	}
	if len(ivBytes) != aes.BlockSize {
		return nil, errs.BadRequest(fmt.Sprintf("wechat crypto: invalid iv length, expected %d bytes", aes.BlockSize))
	}

	cipherBytes, err := base64.StdEncoding.DecodeString(encryptedData)
	if err != nil {
		return nil, errs.BadRequest("wechat crypto: invalid base64 encrypted_data")
	}
	if len(cipherBytes) == 0 || len(cipherBytes)%aes.BlockSize != 0 {
		return nil, errs.BadRequest("wechat crypto: invalid encrypted_data length, not aligned to block size")
	}

	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("wechat crypto: create aes cipher failed: %w", err))
	}

	plainText := make([]byte, len(cipherBytes))
	mode := cipher.NewCBCDecrypter(block, ivBytes)
	mode.CryptBlocks(plainText, cipherBytes)

	unpadded, err := pkcs7Unpad(plainText, aes.BlockSize)
	if err != nil {
		return nil, errs.BadRequest("wechat crypto: invalid pkcs7 padding in decrypted data")
	}

	return unpadded, nil
}

// VerifySignature 校验微信用户数据的 SHA-1 签名（sha1(rawData + sessionKey)）。
//
// 使用 subtle.ConstantTimeCompare 预防计时侧信道攻击。
func VerifySignature(sessionKey, rawData, signature string) bool {
	if sessionKey == "" || signature == "" {
		return false
	}

	//nolint:gosec // WeChat requires SHA-1 for signature verification.
	sum := sha1.Sum([]byte(rawData + sessionKey))
	computed := hex.EncodeToString(sum[:])

	return subtle.ConstantTimeCompare([]byte(computed), []byte(signature)) == 1
}

// pkcs7Unpad 剥离 PKCS#7 填充。
func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	length := len(data)
	if length == 0 || length%blockSize != 0 {
		return nil, errors.New("invalid data length")
	}

	padding := data[length-1]
	padLen := int(padding)
	if padLen == 0 || padLen > blockSize || padLen > length {
		return nil, errors.New("invalid padding value")
	}

	// 验证所有 padding 字节是否一致
	for i := length - padLen; i < length; i++ {
		if data[i] != padding {
			return nil, errors.New("inconsistent padding bytes")
		}
	}

	return data[:length-padLen], nil
}
