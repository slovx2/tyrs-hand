package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

type SSHCredential struct{ ent.Schema }

func (SSHCredential) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "ssh_credentials"}}
}

func (SSHCredential) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.String("name").Unique(),
		field.UUID("secret_id", uuid.UUID{}).Unique(), field.String("public_key"),
		field.String("fingerprint").Unique(), field.Bool("enabled").Default(true),
		field.Int64("version").Default(1), field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

type SSHHost struct{ ent.Schema }

func (SSHHost) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "ssh_hosts"}}
}

func (SSHHost) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.String("alias").Unique(),
		field.String("hostname"), field.Int("port").Default(22), field.String("username"),
		field.UUID("credential_id", uuid.UUID{}),
		field.UUID("proxy_jump_host_id", uuid.UUID{}).Optional().Nillable(),
		field.Bool("enabled").Default(true), field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}
