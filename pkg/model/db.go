package model

import (
	"gorm.io/gorm"
	"reflect"
)

var (
	DB     *gorm.DB
	Models []interface{}
)

// RegisterModel 注册数据库模型
func RegisterModel(m interface{}) {
	Models = append(Models, m)
}

// GetModelByName 根据名称获取模型实例
func GetModelByName(name string) interface{} {
	for _, m := range Models {
		t := reflect.TypeOf(m)
		if t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		if t.Name() == name {
			return m
		}
	}
	return nil
}
