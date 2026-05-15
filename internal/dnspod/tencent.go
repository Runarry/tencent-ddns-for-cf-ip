package dnspod

import (
	"context"
	"fmt"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/errors"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	tcdns "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/dnspod/v20210323"
)

type TencentClient struct {
	cfg    Config
	client *tcdns.Client
}

func NewClient(cfg Config) (*TencentClient, error) {
	credential := common.NewCredential(cfg.SecretID, cfg.SecretKey)
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = "dnspod.tencentcloudapi.com"
	client, err := tcdns.NewClient(credential, "", cpf)
	if err != nil {
		return nil, err
	}
	return &TencentClient{cfg: cfg, client: client}, nil
}

func (c *TencentClient) ListRecords(ctx context.Context) ([]Record, error) {
	request := tcdns.NewDescribeRecordListRequest()
	request.Domain = common.StringPtr(c.cfg.Domain)
	request.Limit = common.Uint64Ptr(3000)
	response, err := c.client.DescribeRecordListWithContext(ctx, request)
	if err != nil {
		return nil, unwrapTencentError(err)
	}
	records := make([]Record, 0, len(response.Response.RecordList))
	for _, item := range response.Response.RecordList {
		records = append(records, Record{
			ID:        valueUint64(item.RecordId),
			Name:      valueString(item.Name),
			Type:      valueString(item.Type),
			Value:     valueString(item.Value),
			Line:      valueString(item.Line),
			TTL:       valueUint64(item.TTL),
			Status:    valueString(item.Status),
			UpdatedOn: valueString(item.UpdatedOn),
		})
	}
	return records, nil
}

func (c *TencentClient) CreateRecord(ctx context.Context, record Record) (uint64, error) {
	request := tcdns.NewCreateRecordRequest()
	request.Domain = common.StringPtr(c.cfg.Domain)
	request.SubDomain = common.StringPtr(record.Name)
	request.RecordType = common.StringPtr(record.Type)
	request.RecordLine = common.StringPtr(c.recordLine(record))
	request.Value = common.StringPtr(record.Value)
	request.TTL = common.Uint64Ptr(c.ttl(record))
	response, err := c.client.CreateRecordWithContext(ctx, request)
	if err != nil {
		return 0, unwrapTencentError(err)
	}
	return valueUint64(response.Response.RecordId), nil
}

func (c *TencentClient) ModifyRecord(ctx context.Context, record Record) error {
	request := tcdns.NewModifyRecordRequest()
	request.Domain = common.StringPtr(c.cfg.Domain)
	request.RecordId = common.Uint64Ptr(record.ID)
	request.SubDomain = common.StringPtr(record.Name)
	request.RecordType = common.StringPtr(record.Type)
	request.RecordLine = common.StringPtr(c.recordLine(record))
	request.Value = common.StringPtr(record.Value)
	request.TTL = common.Uint64Ptr(c.ttl(record))
	_, err := c.client.ModifyRecordWithContext(ctx, request)
	return unwrapTencentError(err)
}

func (c *TencentClient) DeleteRecord(ctx context.Context, recordID uint64) error {
	request := tcdns.NewDeleteRecordRequest()
	request.Domain = common.StringPtr(c.cfg.Domain)
	request.RecordId = common.Uint64Ptr(recordID)
	_, err := c.client.DeleteRecordWithContext(ctx, request)
	return unwrapTencentError(err)
}

func (c *TencentClient) recordLine(record Record) string {
	if record.Line != "" {
		return record.Line
	}
	return c.cfg.RecordLine
}

func (c *TencentClient) ttl(record Record) uint64 {
	if record.TTL > 0 {
		return record.TTL
	}
	return c.cfg.TTL
}

func unwrapTencentError(err error) error {
	if err == nil {
		return nil
	}
	if sdkErr, ok := err.(*errors.TencentCloudSDKError); ok {
		return fmt.Errorf("%s: %s", sdkErr.Code, sdkErr.Message)
	}
	return err
}

func valueString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func valueUint64(value *uint64) uint64 {
	if value == nil {
		return 0
	}
	return *value
}
