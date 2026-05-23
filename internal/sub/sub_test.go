package sub

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	data := `#name: Test Sub
#refresh: 1h
#color: #123456

freeturn://vk?tcp<mode=tcpfwd>@1.1.1.1:56000#key$comment1
##name: Server 1
##ip: 1.1.1.1

freeturn://vk@2.2.2.2
##name: Server 2
`
	s, err := Parse(strings.NewReader(data))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if s.Name != "Test Sub" {
		t.Errorf("Sub.Name = %v, want Test Sub", s.Name)
	}
	if s.Refresh != "1h" {
		t.Errorf("Sub.Refresh = %v, want 1h", s.Refresh)
	}
	if s.Color != "#123456" {
		t.Errorf("Sub.Color = %v, want #123456", s.Color)
	}

	if len(s.Nodes) != 2 {
		t.Fatalf("len(Nodes) = %d, want 2", len(s.Nodes))
	}

	n1 := s.Nodes[0]
	if n1.Name != "Server 1" {
		t.Errorf("Node[0].Name = %v, want Server 1", n1.Name)
	}
	if n1.IP != "1.1.1.1" {
		t.Errorf("Node[0].IP = %v, want 1.1.1.1", n1.IP)
	}
	if n1.URI.Provider != "vk" || n1.URI.Transport != "tcp" {
		t.Errorf("Node[0].URI = %+v", n1.URI)
	}

	n2 := s.Nodes[1]
	if n2.Name != "Server 2" {
		t.Errorf("Node[1].Name = %v, want Server 2", n2.Name)
	}
	if n2.URI.Peer != "2.2.2.2" {
		t.Errorf("Node[1].URI.Peer = %v, want 2.2.2.2", n2.URI.Peer)
	}
}
