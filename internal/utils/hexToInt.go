package utils

import (
	"log"
	"math/big"
)

func HexToInt(hex string) int64 {
	val, ok := new(big.Int).SetString(hex, 16)
	if !ok {
		log.Panicf("Error converting hex %v\n", hex)
	}

	return val.Int64()
}
