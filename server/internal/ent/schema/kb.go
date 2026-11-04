package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// KnowledgeBase 知识库（FR-16）。访问控制全部走 acl 表（resource_type=kb）；删除为软删除，
// 同时物理删除其文件、chunk 与原件（M1 第四块待确认第 10 条）。
type KnowledgeBase struct{ ent.Schema }

func (KnowledgeBase) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "knowledge_bases"}}
}

func (KnowledgeBase) Fields() []ent.Field {
	return []ent.Field{
		field.Int("org_id"),
		field.String("name").NotEmpty().MaxLen(100),
		field.String("description").Default("").MaxLen(500),
		// embedding_model 平台模型名（模型管理中登记）；输出维度必须是 1024（kb_chunks.embedding 为 vector(1024)）。
		field.String("embedding_model").NotEmpty(),
		// 切分参数（字符数）：默认 800 / 100，见 docs/progress/m1-block4.md 待确认第 3 条。
		field.Int("chunk_size").Default(800),
		field.Int("chunk_overlap").Default(100),
		field.Int("created_by"),
		field.Time("deleted_at").Optional().Nillable(),
		// strict_grounding「仅依据资料回答」（D-42）：开启后，对话绑定了该库但检索不到可用资料时不调用模型、不作答。
		field.Bool("strict_grounding").Default(false),
	}
}

func (KnowledgeBase) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (KnowledgeBase) Indexes() []ent.Index {
	return []ent.Index{index.Fields("org_id", "deleted_at")}
}

// KBFile 知识库文件。原件存对象存储（object_key），解析与索引在 worker 中异步进行（asynq 任务 kb:index）。
// acl_custom=false 时继承知识库权限；为 true 时只看 acl 表中 resource_type=kb_file 的授权。
type KBFile struct{ ent.Schema }

func (KBFile) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "kb_files"}}
}

func (KBFile) Fields() []ent.Field {
	return []ent.Field{
		field.Int("org_id"),
		field.Int("kb_id"),
		field.String("name").MaxLen(255),
		field.String("kind"),
		field.Int64("size"),
		field.String("sha256"),
		field.String("object_key"),
		field.Enum("status").Values("pending", "parsing", "indexing", "ready", "failed").Default("pending"),
		// error_code 机器可读的失败原因（empty / timeout / parse_failed / embed_failed / dim_mismatch / ...），error 为中文提示。
		field.String("error_code").Default(""),
		field.String("error").Default("").MaxLen(500),
		field.Int("chars").Default(0),
		field.Int("chunk_count").Default(0),
		field.Bool("acl_custom").Default(false),
		field.Int("uploaded_by"),
		// 本次索引用的 embedding 模型（更换模型重建时用于判断是否已是新索引）。
		field.String("embedding_model").Default(""),
		field.Int("attempts").Default(0),
		field.Time("indexed_at").Optional().Nillable(),
	}
}

func (KBFile) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (KBFile) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("kb_id", "sha256").Unique(),
		index.Fields("kb_id", "name").Unique(),
		index.Fields("org_id", "status"),
	}
}
