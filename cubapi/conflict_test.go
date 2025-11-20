package cubapi

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"testing"

	"github.com/cockroachdb/errors"
	goclientnew "github.com/confighub/sdk/openapi/goclient-new"
)

func TestHandleConflict(t *testing.T) {
	maxRetries := 3

	testcases := map[string]struct {
		in struct {
			resp            []APIResponse
			currentResource any
			actualResource  any
			err             error
		}
		out struct {
			resp    APIResponse
			err     error
			retries int
		}
	}{
		"200 OK": {
			in: struct {
				resp            []APIResponse
				currentResource any
				actualResource  any
				err             error
			}{
				resp: []APIResponse{
					&goclientnew.UpdateUnitResponse{
						HTTPResponse: &http.Response{
							StatusCode: http.StatusOK,
						},
						JSON200: &goclientnew.Unit{},
					},
				},
			},
			out: struct {
				resp    APIResponse
				err     error
				retries int
			}{
				resp: &goclientnew.UpdateUnitResponse{
					HTTPResponse: &http.Response{
						StatusCode: http.StatusOK,
					},
					JSON200: &goclientnew.Unit{},
				},
				retries: 1,
			},
		},
		"409 CONFLICT reconcilable": {
			in: struct {
				resp            []APIResponse
				currentResource any
				actualResource  any
				err             error
			}{
				resp: []APIResponse{
					&goclientnew.UpdateUnitResponse{
						HTTPResponse: &http.Response{
							StatusCode: http.StatusConflict,
						},
						JSON409: &goclientnew.StandardErrorResponse{},
					},
					&goclientnew.UpdateUnitResponse{
						HTTPResponse: &http.Response{
							StatusCode: http.StatusOK,
						},
						JSON200: &goclientnew.Unit{},
					},
				},
				currentResource: &goclientnew.Unit{
					Version: 1,
				},
				actualResource: &goclientnew.Unit{
					Version: 2,
				},
			},
			out: struct {
				resp    APIResponse
				err     error
				retries int
			}{
				err:     ErrMaxConflictRetries,
				retries: 2,
			},
		},
		"409 CONFLICT unreconcilable": {
			in: struct {
				resp            []APIResponse
				currentResource any
				actualResource  any
				err             error
			}{
				resp: []APIResponse{
					&goclientnew.UpdateUnitResponse{
						HTTPResponse: &http.Response{
							StatusCode: http.StatusConflict,
						},
						JSON409: &goclientnew.StandardErrorResponse{},
					},
				},
			},
			out: struct {
				resp    APIResponse
				err     error
				retries int
			}{
				resp:    &goclientnew.UpdateUnitResponse{},
				retries: 1,
			},
		},
		"500 INTERNAL SERVER ERROR": {
			in: struct {
				resp            []APIResponse
				currentResource any
				actualResource  any
				err             error
			}{
				resp: []APIResponse{&goclientnew.UpdateUnitResponse{}},
			},
			out: struct {
				resp    APIResponse
				err     error
				retries int
			}{
				resp:    &goclientnew.UpdateUnitResponse{},
				retries: 1,
			},
		},
		"transport error": {
			in: struct {
				resp            []APIResponse
				currentResource any
				actualResource  any
				err             error
			}{
				err: net.ErrClosed,
			},
			out: struct {
				resp    APIResponse
				err     error
				retries int
			}{
				err:     net.ErrClosed,
				retries: 1,
			},
		},
	}

	for tname, tc := range testcases {
		t.Run(tname, func(t *testing.T) {
			ctx := context.WithValue(
				context.WithValue(
					context.Background(),
					"retries",
					maxRetries,
				),
				"backoff",
				0,
			)

			retries := 0

			resp, err := HandleConflict(
				ctx,
				func() (APIResponse, error) {
					retries++

					var resp APIResponse

					if len(tc.in.resp) > 0 {
						resp = tc.in.resp[0]
						if len(tc.in.resp) > 1 {
							tc.in.resp = tc.in.resp[1:]
						} else {
							tc.in.resp = nil
						}
					}

					return resp, tc.in.err
				},
				func() (any, any, error) {
					return tc.in.currentResource, tc.in.actualResource, nil
				},
				func(_ int) {},
			)

			if err != nil && !errors.Is(err, tc.out.err) {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(resp, tc.out.resp) {
				t.Errorf("actual %+v != expected %+v", resp, tc.out.resp)
			}

			if retries != tc.out.retries {
				t.Errorf("actual %d != expected %d", retries, tc.out.retries)
			}
		})
	}
}

func TestCompareResources(t *testing.T) {
	testcases := map[string]struct {
		in struct {
			original any
			current  any
			actual   any
		}
		out struct {
			conflicts map[string]any
			ok        bool
		}
	}{
		"same": {
			in: struct {
				original any
				current  any
				actual   any
			}{
				original: &goclientnew.Unit{},
				current:  &goclientnew.Unit{},
				actual:   &goclientnew.Unit{},
			},
			out: struct {
				conflicts map[string]any
				ok        bool
			}{
				ok: true,
			},
		},
		"version only": {
			in: struct {
				original any
				current  any
				actual   any
			}{
				original: &goclientnew.Unit{
					Version: 1,
				},
				current: &goclientnew.Unit{
					Version: 1,
				},
				actual: &goclientnew.Unit{
					Version: 2,
				},
			},
			out: struct {
				conflicts map[string]any
				ok        bool
			}{
				conflicts: map[string]any{
					"Version": int64(2),
				},
				ok: true,
			},
		},
		"no conflict": {
			in: struct {
				original any
				current  any
				actual   any
			}{
				original: &goclientnew.Unit{
					Version: 1,
				},
				current: &goclientnew.Unit{
					Labels: map[string]string{
						"test": "test",
					},
					Version: 1,
				},
				actual: &goclientnew.Unit{
					Annotations: map[string]string{
						"test": "test",
					},
					Version: 2,
				},
			},
			out: struct {
				conflicts map[string]any
				ok        bool
			}{
				conflicts: map[string]any{
					"Version": int64(2),
				},
				ok: true,
			},
		},
		"conflicting fields": {
			in: struct {
				original any
				current  any
				actual   any
			}{
				original: &goclientnew.Unit{
					Version: 1,
				},
				current: &goclientnew.Unit{
					Labels: map[string]string{
						"test": "test",
					},
					Version: 1,
				},
				actual: &goclientnew.Unit{
					Labels: map[string]string{
						"test2": "test2",
					},
					Version: 2,
				},
			},
			out: struct {
				conflicts map[string]any
				ok        bool
			}{
				conflicts: map[string]any{
					"Labels": map[string]string{
						"test2": "test2",
					},
					"Version": int64(2),
				},
				ok: false,
			},
		},
	}

	for tname, tc := range testcases {
		t.Run(tname, func(t *testing.T) {
			conflicts, ok := compareResources(tc.in.original, tc.in.current, tc.in.actual)

			for k, v := range tc.out.conflicts {
				if actualV, ok := conflicts[k]; !ok || !reflect.DeepEqual(actualV, v) {
					fmt.Println(ok)
					fmt.Println(reflect.TypeOf(actualV))
					fmt.Println(reflect.TypeOf(v))
					t.Fatalf("conflicts: actual %+v does not equal expected %+v", conflicts, tc.out.conflicts)
				}
			}

			if ok != tc.out.ok {
				t.Errorf("ok: actual %v does not equal expected %v", ok, tc.out.ok)
			}
		})
	}
}
