package transformers

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"

	"github.com/go-courier/reflectx/typesutil"
	"github.com/go-courier/validator/errors"
	"github.com/stretchr/testify/require"
)

type S string

func (s *S) UnmarshalText(data []byte) error {
	return fmt.Errorf("err")
}

func TestJSONTransformer(t *testing.T) {
	data := struct {
		Data struct {
			S           S    `json:"s,omitempty"`
			Bool        bool `json:"bool"`
			StructSlice []struct {
				Name string `json:"name"`
			} `json:"structSlice"`
			StringSlice []string `json:"stringSlice"`
			NestedSlice []struct {
				Names []string `json:"names"`
			} `json:"nestedSlice"`
		} `json:"data"`
	}{}

	ct, _ := TransformerMgrDefault.NewTransformer(typesutil.FromRType(reflect.TypeOf(data)), TransformerOption{})

	{
		b := bytes.NewBuffer(nil)
		_, err := ct.EncodeToWriter(b, data)
		require.NoError(t, err)
	}

	{
		b := bytes.NewBuffer(nil)
		_, err := ct.EncodeToWriter(b, reflect.ValueOf(data))
		require.NoError(t, err)
	}

	{
		b := bytes.NewBufferString(`{`)
		err := ct.DecodeFromReader(b, &data)
		require.Error(t, err)
	}

	{
		b := bytes.NewBufferString(`{}`)
		err := ct.DecodeFromReader(b, reflect.ValueOf(&data))
		require.NoError(t, err)
	}

	cases := []struct {
		json     string
		location string
	}{{
		`
{
 	"data": {
		"s":   "111"
	}
}
`, "data.s",
	},
		{
			`
{
 	"data": {
		"bool":   ""
	}
}
`, "data.bool",
		},
		{
			`
{
		"data": {
			"structSlice": [
				{"name":"{"},
				{"name":"1"},
				{"name": { "test": 1 }},
				{"name":"1"}
			]
		}
}`,
			"data.structSlice[2].name",
		},
		{
			`
		{
			"data": {
				"stringSlice":["1","2",3]
			}
		}`,
			"data.stringSlice[2]",
		},
		{
			`
		{
			"data": {
				"stringSlice":["1","2",3]
			}
		}`,
			"data.stringSlice[2]",
		},
		{
			`
		{
			"data": {
				"bool": true,
				"nestedSlice": [
					{ "names": ["1","2","3"] },
			        { "names": ["1","\"2", 3] }
				]
			}
		}
		`, "data.nestedSlice[1].names[2]",
		},
	}

	for _, c := range cases {
		b := bytes.NewBufferString(c.json)
		err := ct.DecodeFromReader(b, &data)
		err.(*errors.ErrorSet).Each(func(fieldErr *errors.FieldError) {
			require.Equal(t, c.location, fieldErr.Field.String())
		})
	}
}
