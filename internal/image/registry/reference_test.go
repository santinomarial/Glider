package registry

import "github.com/santinomarial/glider/internal/image/reference"

func referenceForTest(value string) (reference.Reference, error) { return reference.Parse(value) }
