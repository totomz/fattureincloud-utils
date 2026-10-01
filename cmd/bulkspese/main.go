package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	fiu "github.com/totomz/fattureincloud-utils"

	"github.com/fattureincloud/fattureincloud-go-sdk/v2/api"
	"github.com/fattureincloud/fattureincloud-go-sdk/v2/model"
)

func main() {
	token := strings.TrimSpace(fiu.AuthToken)
	ctx := context.WithValue(context.Background(), api.ContextAccessToken, token)
	client := api.NewAPIClient(api.NewConfiguration())

	// hai due companies
	companiesResp, _, err := client.UserAPI.ListUserCompanies(ctx).Execute()
	if err != nil {
		slog.Error("failed to list user companies", "err", err)
		os.Exit(1)
	}

	companies := companiesResp.GetData().Companies
	slog.Info("companies found", "count", len(companies))

	// stdin := bufio.NewReader(os.Stdin)

	for _, company := range companies {
		companyID := company.GetId()
		slog.Info("processing company", "company", *company.Name.Get())

		conto := "-"
		switch *company.Name.Get() {
		case "Tommaso Doninelli":
			conto = "fineco"
			// skippa se hai gia processato
			// slog.Info("skippo ", "company", *company.Name.Get())
			// continue
			break
		case "CroccoCode srl":
			conto = "Qonto"
			// skippa se hai gia processato
			// slog.Info("skippo ", "company", *company.Name.Get())
			// continue
			break
		default:
			slog.Info("skippo ", "company", *company.Name.Get())
		}

		finecoID, err := findPaymentAccountID(ctx, client, companyID, conto)
		if err != nil {
			panic(fmt.Errorf("payment not found %s: %w", conto, err))
		}

		// registraSpese(ctx, client, companyID, company.GetName(), finecoID)
		registraCrediti(ctx, client, companyID, company.GetName(), finecoID)

		continue
	}
}

// registraSpese si prende tutte le spese, da registrare e registrate, e le segna come pagate il giorno stesso
func registraSpese(ctx context.Context, client *api.APIClient, companyID int32, companyName string, contoId int32) {

	// fmt.Printf("=== SPESE REGISTRATE %s ===\n", year)
	page := int32(1)
	for {
		resp, _, err := client.ReceivedDocumentsAPI.ListReceivedDocuments(ctx, companyID).Type_("expense").Fieldset("detailed").PerPage(100).Page(page).Execute()
		if err != nil {
			slog.Error("failed to list received documents",
				"company_id", companyID, "company", companyName, "page", page, "err", err)
			break
		}

		for _, doc := range resp.GetData() {
			slog.Info("spesa",
				"supplier", doc.Entity.GetName(),
				"date", doc.GetDate(),
				"description", doc.GetDescription(),
				"amount_net", doc.GetAmountNet(),
				"payments_list", len(doc.GetPaymentsList()),
			)

			if doc.GetAmountNet() == 0 {
				slog.Info("    -> credito, skippo")
				continue
			}

			// check if has been payed already
			paid := false
			for _, payment := range doc.PaymentsList {
				if payment.GetStatus() == "paid" || payment.GetStatus() == "reversed" {
					paid = true
					slog.Info("    -> pagata!", "status", payment.GetStatus(), "il", payment.GetPaidDate())
					continue
				}
			}

			if paid {
				continue
			}

			registerReceived(ctx, client, companyID, doc, contoId)
			slog.Info("processata")
		}

		if page >= resp.GetLastPage() {
			break
		}
		page++
	}

	page = 1
	for {
		resp, _, err := client.ReceivedDocumentsAPI.ListPendingReceivedDocuments(ctx, companyID).Type_("expense").Fieldset("detailed").PerPage(100).Page(page).Execute()
		if err != nil {
			slog.Error("failed to list pending received documents", "company_id", companyID, "company", companyName, "page", page, "err", err)
			return
		}

		for _, doc := range resp.GetData() {
			slog.Info("spesa", "id", doc.GetId(), "date", doc.GetDate(), "subject", doc.GetSubject(), "gross", doc.GetAmountGross(), "supplier", doc.GetSupplierName())
			registerPending(ctx, client, companyID, doc, contoId)
			slog.Info("pagata?")
		}

		if page >= resp.GetLastPage() {
			break
		}
		page++
	}
}

// registraCrediti si prende tutte le fatture emesse non ancora incassate e le segna come incassate il giorno stesso
func registraCrediti(ctx context.Context, client *api.APIClient, companyID int32, companyName string, contoId int32) {
	page := int32(1)
	for {
		resp, _, err := client.IssuedDocumentsAPI.ListIssuedDocuments(ctx, companyID).Type_("invoice").Fieldset("detailed").PerPage(100).Page(page).Execute()
		if err != nil {
			slog.Error("failed to list issued documents", "company_id", companyID, "company", companyName, "page", page, "err", err)
			break
		}

		for _, doc := range resp.GetData() {
			slog.Info("credito", "client", doc.Entity.GetName(), "date", doc.GetDate(), "number", doc.GetNumber(), "amount_net", doc.GetAmountNet(), "payments_list", len(doc.GetPaymentsList()))

			if doc.GetAmountNet() == 0 {
				slog.Info("    -> importo zero, skippo")
				continue
			}

			// check if has been collected already
			paid := false
			for _, payment := range doc.PaymentsList {
				if payment.GetStatus() == model.IssuedDocumentStatuses.PAID || payment.GetStatus() == model.IssuedDocumentStatuses.REVERSED {
					paid = true
					slog.Info("    -> incassata!", "status", payment.GetStatus(), "il", payment.GetPaidDate())
				}
			}

			if paid {
				continue
			}

			registerIssued(ctx, client, companyID, doc, contoId)
			slog.Info("processata")
		}

		if page >= resp.GetLastPage() {
			break
		}
		page++
	}
}

// registerIssued marks every existing payment of an issued document as paid on the document date, keeping ids, amounts and due dates
func registerIssued(ctx context.Context, client *api.APIClient, companyID int32, doc model.IssuedDocument, paymentAccountID int32) {
	date, _, _ := strings.Cut(doc.GetDate(), " ")

	account := model.NewPaymentAccount().SetId(paymentAccountID)
	payments := make([]model.IssuedDocumentPaymentsListItem, 0, len(doc.GetPaymentsList()))
	for _, payment := range doc.GetPaymentsList() {
		payment.SetStatus(model.IssuedDocumentStatuses.PAID).SetPaidDate(date).SetPaymentAccount(*account)
		payments = append(payments, payment)
	}
	data := model.NewIssuedDocument().SetPaymentsList(payments)
	req := model.NewModifyIssuedDocumentRequest().SetData(*data)

	_, httpResp, err := client.IssuedDocumentsAPI.ModifyIssuedDocument(ctx, companyID, doc.GetId()).ModifyIssuedDocumentRequest(*req).Execute()
	for httpResp != nil && httpResp.StatusCode == http.StatusTooManyRequests {
		slog.Info("rate limited, retrying in 60s", "document_id", doc.GetId())
		time.Sleep(60 * time.Second)
		_, httpResp, err = client.IssuedDocumentsAPI.ModifyIssuedDocument(ctx, companyID, doc.GetId()).ModifyIssuedDocumentRequest(*req).Execute()
	}
	if err != nil {
		slog.Error("failed to mark issued document as paid", "company_id", companyID, "document_id", doc.GetId(), "client", doc.Entity.GetName(), "err", err)
		return
	}

	slog.Info("    -> incassata", "document_id", doc.GetId())
}

// findPaymentAccountID returns the id of the only payment account whose name
// contains nameContains (case insensitive). Zero or multiple matches are errors.
func findPaymentAccountID(ctx context.Context, client *api.APIClient, companyID int32, nameContains string) (int32, error) {
	resp, _, err := client.InfoAPI.ListPaymentAccounts(ctx, companyID).Execute()
	if err != nil {
		return 0, err
	}

	var matches []model.PaymentAccount
	for _, account := range resp.GetData() {
		if strings.Contains(strings.ToLower(account.GetName()), strings.ToLower(nameContains)) {
			matches = append(matches, account)
		}
	}
	if len(matches) != 1 {
		return 0, errors.New("expected exactly one payment account matching " + nameContains + ", found " + strconv.Itoa(len(matches)))
	}
	return matches[0].GetId(), nil
}

// registerPending turns a pending received document into a registered expense,
// with a single payment already paid on the document date from the given account.
func registerPending(ctx context.Context, client *api.APIClient, companyID int32, pending model.PendingReceivedDocument, paymentAccountID int32) {

	// Pending date is "YYYY-MM-DD hh:mm:ss", the API wants "YYYY-MM-DD".
	date, _, _ := strings.Cut(pending.GetDate(), " ")

	account := model.NewPaymentAccount().SetId(paymentAccountID)
	payment := model.NewReceivedDocumentPaymentsListItem().
		SetAmount(pending.GetAmountGross()).
		SetDueDate(date).
		SetPaidDate(date).
		SetStatus(string(model.IssuedDocumentStatuses.PAID)).
		SetPaymentAccount(*account)

	entity := model.NewEntity().SetName(pending.GetSupplierName())
	data := model.NewReceivedDocument().
		SetType(pending.GetDocumentType()).
		SetEntity(*entity).
		SetDate(date).
		SetAmountNet(pending.GetAmountNet()).
		SetAmountVat(pending.GetAmountVat()).
		SetPaymentsList([]model.ReceivedDocumentPaymentsListItem{*payment})

	req := model.NewCreateReceivedDocumentRequest().
		SetPendingId(pending.GetId()).
		SetData(*data)

	_, _, err := client.ReceivedDocumentsAPI.
		CreateReceivedDocument(ctx, companyID).
		CreateReceivedDocumentRequest(*req).
		Execute()
	if err != nil {
		slog.Error("failed to register pending document", "company_id", companyID, "pending_id", pending.GetId(), "subject", pending.GetSubject(), "err", err)
		return
	}

	// created := resp.GetData()
	slog.Info("    -> registered")
}

// registerReceived updates an already registered document, replacing its payments with a single payment marked as paid on the document date
func registerReceived(ctx context.Context, client *api.APIClient, companyID int32, doc model.ReceivedDocument, paymentAccountID int32) {
	date, _, _ := strings.Cut(doc.GetDate(), " ")

	account := model.NewPaymentAccount().SetId(paymentAccountID)
	payment := model.NewReceivedDocumentPaymentsListItem().SetAmount(doc.GetAmountGross()).SetDueDate(date).SetPaidDate(date).SetStatus(string(model.IssuedDocumentStatuses.PAID)).SetPaymentAccount(*account)
	data := model.NewReceivedDocument().SetPaymentsList([]model.ReceivedDocumentPaymentsListItem{*payment})
	req := model.NewModifyReceivedDocumentRequest().SetData(*data)

	_, httpResp, err := client.ReceivedDocumentsAPI.ModifyReceivedDocument(ctx, companyID, doc.GetId()).ModifyReceivedDocumentRequest(*req).Execute()
	for httpResp != nil && httpResp.StatusCode == http.StatusTooManyRequests {
		slog.Info("rate limited, retrying in 60s", "document_id", doc.GetId())
		time.Sleep(60 * time.Second)
		_, httpResp, err = client.ReceivedDocumentsAPI.ModifyReceivedDocument(ctx, companyID, doc.GetId()).ModifyReceivedDocumentRequest(*req).Execute()
	}
	if err != nil {
		slog.Error("failed to mark received document as paid", "company_id", companyID, "document_id", doc.GetId(), "supplier", doc.Entity.GetName(), "err", err)
		return
	}

	slog.Info("    -> paid", "document_id", doc.GetId())
}
