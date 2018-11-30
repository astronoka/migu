package migu

// Column is table column definition.
type Column struct {
	Name          string
	Type          string
	Comment       string
	Unique        bool
	PrimaryKey    bool
	AutoIncrement bool
	Ignore        bool
	Default       string
	Size          uint64
}
