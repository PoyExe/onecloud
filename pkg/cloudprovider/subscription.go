// Copyright 2019 Yunion
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cloudprovider

type SubscriptionCreateInput struct {
	Name                string
	EnrollmentAccountId string
	OfferType           string
}

type SEnrollmentAccountProperties struct {
	// Enrollment Account 主体名称
	PrincipalName string

	// Enrollment Account 支持创建订阅类别
	OfferTypes []string
}

type SEnrollmentAccount struct {
	// Enrollment Account 标识Id
	Id string

	// Enrollment Account name
	Name string

	// Enrollment Account 类型
	Type string

	// Enrollment Account 属性
	Properties SEnrollmentAccountProperties
}