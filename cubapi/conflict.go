package cubapi

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/cockroachdb/errors"
)

const (
	// defaultMaxRetries is the default number of attempts at the conflicting operation
	defaultMaxRetries = -1
	// defaultBackoffInterval is the duration to wait between attempts, starting at 0, then attempts * interval
	defaultBackoffInterval = time.Second
)

var (
	// ErrMaxRetries is returned when the maximum number of retries is reach on
	// a conflicting operation
	ErrMaxConflictRetries = errors.New("max retries of operation reached")
)

// HandleConflict calls `op` which returns an APIResponse, that response is a 409 conflict, it
// will retry the operation, unless the conflict is unreconcilable
func HandleConflict(
	ctx context.Context,
	op func() (APIResponse, error), // the actual update operation
	fetchResourcesForComparison func() (any, any, any, error), // compare the API resource on server to update
	bumpVersion func(version int64), // set the version on the resource for retrying
) (APIResponse, error) {
	retries := defaultMaxRetries
	if retryOverride := ctx.Value("retries"); retryOverride != nil {
		retryCount, ok := retryOverride.(int)
		if ok {
			retries = retryCount
		}
	}

	backoff := defaultBackoffInterval
	if backoffOverride := ctx.Value("backoff"); backoffOverride != nil {
		backoffInterval, ok := backoffOverride.(time.Duration)
		if ok {
			backoff = backoffInterval
		}
	}

	attempts := 0

	for retries == -1 || attempts < retries {
		attempts++

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempts) * backoff):
			resp, err := op()
			if err != nil {
				return resp, err
			}

			if resp.StatusCode() != 409 {
				return resp, nil
			}

			originalResource, currentResource, actualResource, err := fetchResourcesForComparison()
			if err != nil {
				return nil, err
			}

			conflicts, ok := compareResources(originalResource, currentResource, actualResource)
			if !ok { // conflicts
				if conflicts != nil {
					printConflicts(conflicts)
				}
				return resp, nil
			}

			versionUntyped, ok := conflicts["Version"]
			if !ok {
				break
			}

			version, ok := versionUntyped.(int64)
			if !ok {
				break
			}

			bumpVersion(version)
		}
	}

	return nil, ErrMaxConflictRetries
}

// compareResources takes the original resource the update read, the update (current)
// and the actual value on the server and diffs them for an actual conflict, i.e a value that is different
// for both the read value to update, and the actual value on the server and the original
func compareResources(original, current, actual any) (map[string]any, bool) {
	if reflect.DeepEqual(current, actual) { // if everything is equal, immediately return true
		return nil, true
	}

	origV := reflect.ValueOf(original)
	currV := reflect.ValueOf(current)
	actualV := reflect.ValueOf(actual)

	if (currV.Kind() != actualV.Kind() && origV.Kind() != currV.Kind()) || (currV.Type() != actualV.Type() && currV.Type() != origV.Type()) {
		// if the resources are of different types, return false
		return nil, false
	}

	if currV.Kind() == reflect.Pointer { // we already asserted all are the same Kind
		currV = currV.Elem()
		actualV = actualV.Elem()
		origV = origV.Elem()
	}

	diffs := make(map[string]any)

	for i := 0; i < currV.NumField(); i++ {
		currField := currV.Field(i)
		actualField := actualV.Field(i)
		fieldName := currV.Type().Field(i).Name

		if !reflect.DeepEqual(currField.Interface(), actualField.Interface()) { // if a field is not equal, store it
			origField := origV.Field(i)

			serverDelta := !reflect.DeepEqual(origField.Interface(), actualField.Interface())
			clientDelta := !reflect.DeepEqual(origField.Interface(), currField.Interface())
			if serverDelta && clientDelta || fieldName == "Version" { // missing change
				diffs[fieldName] = actualField.Interface()
			}
		}
	}

	if _, ok := diffs["Version"]; !ok || len(diffs) > 1 { // if more than version is different and will conflict, return diffs and false
		return diffs, false
	}

	return diffs, true // not actually conflicting
}

func printConflicts(conflicts map[string]any) {
	fmt.Println("The following are in conflict and would be overwritten on update:")
	for k, v := range conflicts {
		fmt.Printf("	%s = %v\n", k, v)
	}
}
