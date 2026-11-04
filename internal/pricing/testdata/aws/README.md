# Recorded AWS responses

HTTP response bodies replayed by `aws_test.go` through the real AWS SDK clients (no network):

- `pricing/<instance-type>.json`: AWS Pricing `GetProducts` (JSON 1.1) for eu-west-1, Linux, shared
  tenancy, on-demand. `PriceList` items are JSON documents encoded as strings, as AWS returns them.
  `m7i.large.json` + `m7i.large-page2.json` exercise `NextToken` pagination; `m6i.large.json` has a
  malformed `memory` attribute; `empty.json` is the response for an unknown instance type.
- `ec2/<region>/<instance-type>.xml`: EC2 `DescribeSpotPriceHistory` (EC2 query protocol, XML).
  `c6a.2xlarge.xml` includes an older eu-west-1b entry that must be ignored.
- `*/error-throttling.*`: throttling error bodies.

These follow the AWS wire format but were assembled offline (this environment has no AWS access),
with prices matching `dev/prices.yaml`. To refresh from a real account, capture the bodies with
`aws pricing get-products --region us-east-1 --service-code AmazonEC2 --filters ...` and
`aws ec2 describe-spot-price-history --region eu-west-1 --debug` and keep the file names.
