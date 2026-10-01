@README.md

# Developer Standard
- Totomz odia python
- non scrivere mai codice usando pythonismi
- usa sempre codice tipizzato

NON USAFE MAI fmt.Sprintf, usa SEMPRE slog.Info

NON ANDARE MAI ACCAPO porcoddio mai
```go
client.ReceivedDocumentsAPI.ListPendingReceivedDocuments(ctx, companyID).Type_("expense").Fieldset("detailed").PerPage(100).Page(page).Execute()
```
va benissimo se la linea è lunga,  non stampiamo il codice da 70 anni