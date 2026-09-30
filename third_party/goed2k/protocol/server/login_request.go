package server

import (
	"bytes"
	"strings"

	"github.com/goed2k/core/protocol"
)

const (
	opLoginRequest byte = 0x01

	CapableZlib         = 0x0001
	CapableIPInLogin    = 0x0002
	CapableAuxPort      = 0x0004
	CapableNewTags      = 0x0008
	CapableUnicode      = 0x0010
	CapableLargeFile    = 0x0100
	CapableSupportCrypt = 0x0200
	CapableRequestCrypt = 0x0400
	CapableRequireCrypt = 0x0800

	// Soft IDs for CT_EMULE_VERSION high byte (eMule opcodes.h).
	softIDEMule = 0

	ctName         = 0x01
	ctPort         = 0x0F
	ctVersion      = 0x11
	ctServerFlags  = 0x20
	ctEMuleVersion = 0xFB
)

type LoginRequest struct {
	Hash       protocol.Hash
	Point      protocol.Endpoint
	Properties protocol.TagList
}

func (l *LoginRequest) Get(src *bytes.Reader) error {
	hash, err := protocol.ReadHash(src)
	if err != nil {
		return err
	}
	point, err := protocol.ReadEndpoint(src)
	if err != nil {
		return err
	}
	count, err := protocol.ReadUInt32(src)
	if err != nil {
		return err
	}
	l.Hash = hash
	l.Point = point
	l.Properties = make(protocol.TagList, int(count))
	for i := 0; i < int(count); i++ {
		var tag protocol.SimpleTag
		if err := tag.Get(src); err != nil {
			return err
		}
		l.Properties[i] = tag
	}
	return nil
}

func (l LoginRequest) Put(dst *bytes.Buffer) error {
	if err := protocol.WriteHash(dst, l.Hash); err != nil {
		return err
	}
	if err := protocol.WriteEndpoint(dst, l.Point); err != nil {
		return err
	}
	if err := protocol.WriteUInt32(dst, uint32(len(l.Properties))); err != nil {
		return err
	}
	for _, property := range l.Properties {
		if err := property.Put(dst); err != nil {
			return err
		}
	}
	return nil
}

func (l LoginRequest) BytesCount() int {
	size := 16 + 6 + 4
	for _, property := range l.Properties {
		size += property.BytesCount()
	}
	return size
}

func NewLoginRequest(userAgent protocol.Hash, listenPort int, clientName string) LoginRequest {
	return NewLoginRequestWithCrypt(userAgent, listenPort, clientName, false, false)
}

// NewLoginRequestWithCrypt builds OP_LOGINREQUEST shaped like eMule 0.50a:
// ClientID=0 in endpoint, CT_PORT, CT_VERSION=60, full SRVCAP_* flags, and
// CT_EMULE_VERSION with SO_EMULE high byte (not cDonkey/amule IDs that some
// strict servers soft-reject).
func NewLoginRequestWithCrypt(userAgent protocol.Hash, listenPort int, clientName string, supportCrypt, requestCrypt bool) LoginRequest {
	if strings.TrimSpace(clientName) == "" {
		clientName = "eMule"
	}
	// SO_EMULE | 0.50a  → matches official eMule CT_EMULE_VERSION packing.
	versionClient := uint32(softIDEMule<<24) | uint32(0<<17) | uint32(50<<10) | uint32(1<<7)
	capability := uint32(CapableZlib | CapableIPInLogin | CapableAuxPort | CapableNewTags | CapableUnicode | CapableLargeFile)
	if supportCrypt {
		capability |= CapableSupportCrypt
	}
	if requestCrypt {
		capability |= CapableRequestCrypt
	}
	return LoginRequest{
		Hash:  userAgent,
		Point: protocol.NewEndpoint(0, listenPort),
		Properties: protocol.TagList{
			protocol.NewStringTag(ctName, clientName),
			protocol.NewUInt32Tag(ctVersion, 0x3c),
			protocol.NewUInt32Tag(ctPort, uint32(listenPort)),
			protocol.NewUInt32Tag(ctServerFlags, capability),
			protocol.NewUInt32Tag(ctEMuleVersion, versionClient),
		},
	}
}

var _ protocol.Serializable = (*LoginRequest)(nil)
