package dynamodb

import (
	"context"
	"fmt"

	sdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// responseProtocol contains SDK decoder panics at the response boundary. It
// delegates serialization and transport unchanged, and does not parse or copy
// response bodies a second time. Application and reconciler panics are outside
// this boundary.
type responseProtocol struct{ smithyhttp.ClientProtocol }

func (protocol responseProtocol) DeserializeResponse(ctx context.Context, schema *smithy.OperationSchema, registry *smithy.TypeRegistry, response *smithyhttp.Response, output smithy.Deserializable) (err error) {
	defer func() {
		if failure := recover(); failure != nil {
			// Do not include a decoder's panic value: it could contain payload data.
			err = &smithy.DeserializationError{Err: fmt.Errorf("DynamoDB response decoder panicked (%T)", failure)}
		}
	}()
	return protocol.ClientProtocol.DeserializeResponse(ctx, schema, registry, response, output)
}

func containDecoderPanics(options *sdk.Options) {
	options.Protocol = responseProtocol{ClientProtocol: options.Protocol}
}
