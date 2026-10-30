package auth

import (
	"errors"
	"fmt"
	"net/http"
)

type RouterOption struct {
	AuthAgent     *AuthAgent
	RequireAuth   bool                                     //This router require authentication
	DeniedHandler func(http.ResponseWriter, *http.Request) //Things to do when request is rejected
	TargetMux     *http.ServeMux
}

type RouterDef struct {
	option    RouterOption
	endpoints map[string]func(http.ResponseWriter, *http.Request)
}

func NewManagedHTTPRouter(option RouterOption) *RouterDef {
	return &RouterDef{
		option:    option,
		endpoints: map[string]func(http.ResponseWriter, *http.Request){},
	}
}

func (router *RouterDef) HandleFunc(endpoint string, handler func(http.ResponseWriter, *http.Request)) error {
	if _, exist := router.endpoints[endpoint]; exist {
		fmt.Println("WARNING! Duplicated registering of web endpoint: " + endpoint)
		return errors.New("endpoint register duplicated")
	}

	authAgent := router.option.AuthAgent

	if router.option.TargetMux == nil {
		http.HandleFunc(endpoint, func(w http.ResponseWriter, r *http.Request) {
			if router.option.RequireAuth {
				authAgent.HandleCheckAuth(w, r, func(w http.ResponseWriter, r *http.Request) {
					handler(w, r)
				})
			} else {
				handler(w, r)
			}

		})
	} else {
		router.option.TargetMux.HandleFunc(endpoint, func(w http.ResponseWriter, r *http.Request) {
			if router.option.RequireAuth {
				authAgent.HandleCheckAuth(w, r, func(w http.ResponseWriter, r *http.Request) {
					handler(w, r)
				})
			} else {
				handler(w, r)
			}

		})
	}

	router.endpoints[endpoint] = handler

	return nil
}
