package db

import (
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"main/model"
	"main/util"
	"time"
)

type AccountDB struct {
	Client *dynamodb.Client
}

func (receiver AccountDB) Create(account model.Account) error {
	accItem, err := attributevalue.MarshalMap(account)
	if err != nil {
		return err
	}

	keyCond, filter := getKeyConAndFilter(account.PK, account.Type)
	accounts, err := receiver.getAll(keyCond, filter, true)
	if err != nil {
		return err
	}
	if len(accounts) != 0 {
		return util.AlreadyExists
	}

	accInput := &dynamodb.PutItemInput{
		Item:      accItem,
		TableName: aws.String(util.TableName),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = receiver.Client.PutItem(ctx, accInput)
	return err
}

func getKeyConAndFilter(id string, t string) (expression.KeyConditionBuilder, expression.ConditionBuilder) {
	keyCond := expression.KeyAnd(
		expression.Key("PK").Equal(expression.Value(util.GetPK(id))),
		expression.Key("SK").BeginsWith("ACCOUNT#"),
	)
	filter := expression.Name("Type").Equal(expression.Value(t))
	return keyCond, filter
}

func (receiver AccountDB) GetAll(id, t string) ([]model.Account, error) {
	keyCond, _ := getKeyConAndFilter(id, "")

	var filter expression.ConditionBuilder
	isFilter := true
	if t == "open" {
		filter = expression.Name("CloseDate").AttributeNotExists()
	} else if t == "closed" {
		filter = expression.Name("CloseDate").AttributeExists()
	} else {
		isFilter = false
	}
	return receiver.getAll(keyCond, filter, isFilter)
}

func (receiver AccountDB) getAll(keyCond expression.KeyConditionBuilder, filter expression.ConditionBuilder,
	isFilter bool) ([]model.Account, error) {

	var expr expression.Expression
	var err error
	if isFilter {
		expr, err = expression.NewBuilder().WithKeyCondition(keyCond).WithFilter(filter).Build()
	} else {
		expr, err = expression.NewBuilder().WithKeyCondition(keyCond).Build()
	}

	if err != nil {
		return nil, err
	}

	input := &dynamodb.QueryInput{
		TableName:                 aws.String(util.TableName),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		KeyConditionExpression:    expr.KeyCondition(),
	}
	if isFilter {
		input.FilterExpression = expr.Filter()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := receiver.Client.Query(ctx, input)
	if err != nil {
		return nil, err
	}

	if len(result.Items) == 0 {
		return nil, nil
	}

	var accounts []model.Account
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &accounts); err != nil {
		return nil, err
	}
	return accounts, nil
}

func (receiver AccountDB) GetAccount(account model.Account) (model.Account, error) {
	primaryKey := map[string]string{
		"PK": util.GetPK(account.PK),
		"SK": util.GetSK(account.SK),
	}

	pk, err := attributevalue.MarshalMap(primaryKey)
	if err != nil {
		return model.Account{}, err
	}

	input := &dynamodb.GetItemInput{
		Key:            pk,
		TableName:      aws.String(util.TableName),
		ConsistentRead: aws.Bool(true),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := receiver.Client.GetItem(ctx, input)
	if err != nil {
		return model.Account{}, err
	}

	var acc model.Account
	if err := attributevalue.UnmarshalMap(result.Item, &acc); err != nil {
		return model.Account{}, err
	}
	return acc, nil
}

func (receiver AccountDB) depositWithdraw(account model.Account, amount float64, deposit bool) error {
	primaryKey := map[string]string{
		"PK": util.GetPK(account.PK),
		"SK": util.GetSK(account.SK),
	}

	pk, err := attributevalue.MarshalMap(primaryKey)
	if err != nil {
		return err
	}

	acc, err := receiver.GetAccount(account)
	if err != nil || acc.PK == "" {
		return errors.New("invalid account")
	}

	if acc.CloseDate != nil && !acc.CloseDate.IsZero() {
		return errors.New("account is closed")
	}

	cond := expression.Name("CloseDate").AttributeNotExists()

	var upd expression.UpdateBuilder
	var expr expression.Expression

	if deposit {
		upd = expression.Set(expression.Name("Amount"), expression.Plus(expression.Name("Amount"),
			expression.Value(amount)))
	} else {
		acc, er := receiver.GetAccount(account)
		if er != nil {
			return er
		}

		if acc.Amount-amount < float64(-1*acc.Limit) {
			return util.InsufficientFounds
		}

		upd = expression.Set(expression.Name("Amount"), expression.Minus(expression.Name("Amount"),
			expression.Value(amount)))
	}
	expr, err = expression.NewBuilder().WithUpdate(upd).WithCondition(cond).Build()

	if err != nil {
		return err
	}

	input := &dynamodb.UpdateItemInput{
		Key:                       pk,
		TableName:                 aws.String(util.TableName),
		ConditionExpression:       expr.Condition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		UpdateExpression:          expr.Update(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = receiver.Client.UpdateItem(ctx, input)
	return err
}

func (receiver AccountDB) Deposit(account model.Account, amount float64) error {
	return receiver.depositWithdraw(account, amount, true)
}

func (receiver AccountDB) Withdraw(account model.Account, amount float64) error {
	return receiver.depositWithdraw(account, amount, false)
}

func (receiver AccountDB) Close(account model.Account) error {
	primaryKey := map[string]string{
		"PK": util.GetPK(account.PK),
		"SK": util.GetSK(account.SK),
	}

	pk, err := attributevalue.MarshalMap(primaryKey)
	if err != nil {
		return err
	}

	acc, err := receiver.GetAccount(account)
	if err != nil || acc.PK == "" {
		return errors.New("invalid account")
	}

	if acc.CloseDate != nil && !acc.CloseDate.IsZero() {
		return errors.New("account is already closed")
	}

	upd := expression.Set(expression.Name("CloseDate"), expression.Value(time.Now().Unix()))
	cond := expression.Name("CloseDate").AttributeNotExists()
	cond2 := expression.Name("PK").Equal(expression.Value(util.GetPK(account.PK)))

	expr, err := expression.NewBuilder().WithUpdate(upd).WithCondition(cond).WithCondition(cond2).Build()
	if err != nil {
		return err
	}

	input := &dynamodb.UpdateItemInput{
		Key:                       pk,
		TableName:                 aws.String(util.TableName),
		ConditionExpression:       expr.Condition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		UpdateExpression:          expr.Update(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = receiver.Client.UpdateItem(ctx, input)
	return err
}

func (receiver AccountDB) Delete(account model.Account) error {
	primaryKey := map[string]string{
		"PK": util.GetPK(account.PK),
		"SK": util.GetSK(account.SK),
	}

	pk, err := attributevalue.MarshalMap(primaryKey)
	if err != nil {
		return err
	}

	acc, err := receiver.GetAccount(account)
	if err != nil || acc.PK == "" {
		return util.InvalidAccount
	}

	if acc.CloseDate == nil {
		return util.OpenAccount
	}

	cond := expression.Name("CloseDate").AttributeExists()
	expr, err := expression.NewBuilder().WithCondition(cond).Build()
	if err != nil {
		return err
	}

	input := &dynamodb.DeleteItemInput{
		Key:                       pk,
		TableName:                 aws.String(util.TableName),
		ConditionExpression:       expr.Condition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = receiver.Client.DeleteItem(ctx, input)
	return err
}
