package fixture

import "github.com/google/uuid"

func Ready() bool { return uuid.New() != uuid.Nil }
