// Copyright © Microsoft <wastore@microsoft.com>
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package e2etest

import (
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azfile/file"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azfile/share"
	"github.com/Azure/azure-storage-azcopy/v10/azbfs"
	"net/url"
	"os"
	"path"
	"runtime"
	"strings"

	"github.com/Azure/azure-storage-azcopy/v10/common"
)

func assertNoStripTopDir(stripTopDir bool) {
	if stripTopDir {
		panic("support for stripTopDir is not yet implemented here") // when implemented, resourceManagers should return /* in the right part of the string
	}
}

type downloadContentOptions struct {
	resourceRelPath string
	downloadBlobContentOptions
	downloadFileContentOptions
}

type downloadBlobContentOptions struct {
	containerClient *container.Client
	cpkInfo         *blob.CPKInfo
	cpkScopeInfo    *blob.CPKScopeInfo
}

type downloadFileContentOptions struct {
	shareClient *share.Client
}

// TODO: any better names for this?
// a source or destination. We need one of these for each of Blob, Azure Files, BlobFS, S3, Local disk etc.
type resourceManager interface {

	// creates an empty container/share/directory etc
	createLocation(a asserter, s *scenario)

	// creates the test files in the location. Implementers can assume that createLocation has been called first.
	// This method may be called multiple times, in which case it should overwrite any like-named files that are already there.
	// (e.g. when test need to create files with later modification dates, they will trigger a second call to this)
	createFiles(a asserter, s *scenario, isSource bool)

	// creates a test file in the location. Same assumptions as createFiles.
	createFile(a asserter, o *testObject, s *scenario, isSource bool)

	// Gets the names and properties of all files (and, if applicable, folders) that exist.
	// Used for verification
	getAllProperties(a asserter) map[string]*objectProperties

	// Download
	downloadContent(a asserter, options downloadContentOptions) []byte

	// cleanup gets rid of everything that setup created
	// (Takes no param, because the resourceManager is expected to track its own state. E.g. "what did I make")
	cleanup(a asserter)

	// gets the azCopy command line param that represents the resource.  withSas is ignored when not applicable
	getParam(stripTopDir bool, withSas bool, withFile string) string

	getSAS() string

	// isContainerLike returns true if the resource is a top-level cloud-based resource (e.g. a container, a File Share, etc)
	isContainerLike() bool

	// appendSourcePath appends a path to creates absolute path
	appendSourcePath(string, bool)

	// create a snapshot for the source, and use it for the job
	createSourceSnapshot(a asserter)
}

// /////////////

type resourceLocal struct {
	dirPath string
}

func (r *resourceLocal) createLocation(a asserter, s *scenario) {
	if r.dirPath == common.Dev_Null {
		return
	}

	r.dirPath = TestResourceFactory{}.CreateLocalDirectory(a)
	if s.GetModifiableParameters().relativeSourcePath != "" {
		r.appendSourcePath(s.GetModifiableParameters().relativeSourcePath, true)
	}
}

func (r *resourceLocal) createFiles(a asserter, s *scenario, isSource bool) {
	if r.dirPath == common.Dev_Null {
		return
	}

	scenarioHelper{}.generateLocalFilesFromList(a, &generateLocalFilesFromList{
		dirPath: r.dirPath,
		generateFromListOptions: generateFromListOptions{
			fs:                      s.fs.allObjects(isSource),
			defaultSize:             s.fs.defaultSize,
			preservePosixProperties: s.p.preservePOSIXProperties,
		},
	})
}

func (r *resourceLocal) createFile(a asserter, o *testObject, s *scenario, isSource bool) {
	if r.dirPath == common.Dev_Null {
		return
	}

	scenarioHelper{}.generateLocalFilesFromList(a, &generateLocalFilesFromList{
		dirPath: r.dirPath,
		generateFromListOptions: generateFromListOptions{
			fs:          []*testObject{o},
			defaultSize: s.fs.defaultSize,
		},
	})
}

func (r *resourceLocal) cleanup(_ asserter) {
	if r.dirPath == common.Dev_Null {
		return
	}

	if r.dirPath != "" {
		_ = os.RemoveAll(r.dirPath)
	}
}

func (r *resourceLocal) getParam(stripTopDir bool, withSas bool, withFile string) string {
	if r.dirPath == common.Dev_Null {
		return common.Dev_Null
	}

	if !stripTopDir {
		if withFile != "" {
			p := path.Join(r.dirPath, withFile)

			if runtime.GOOS == "windows" {
				p = strings.ReplaceAll(p, "/", "\\")
			}

			return p
		}

		return r.dirPath
	}
	return path.Join(r.dirPath, "*")
}

func (r *resourceLocal) getSAS() string {
	return ""
}

func (r *resourceLocal) isContainerLike() bool {
	return false
}

func (r *resourceLocal) appendSourcePath(filePath string, _ bool) {
	r.dirPath += "/" + filePath
}

func (r *resourceLocal) getAllProperties(a asserter) map[string]*objectProperties {
	if r.dirPath == common.Dev_Null {
		return make(map[string]*objectProperties)
	}

	return scenarioHelper{}.enumerateLocalProperties(a, r.dirPath)
}

func (r *resourceLocal) downloadContent(_ asserter, _ downloadContentOptions) []byte {
	panic("Not Implemented")
}

func (r *resourceLocal) createSourceSnapshot(a asserter) {
	panic("Not Implemented")
}

// /////

type resourceBlobContainer struct {
	accountType     AccountType
	containerClient *container.Client
	rawSasURL       *url.URL
}

func (r *resourceBlobContainer) createLocation(a asserter, s *scenario) {
	cu, _, rawSasURL := TestResourceFactory{}.CreateNewContainer(a, s.GetTestFiles().sourcePublic, r.accountType)
	r.containerClient = cu
	rawURL, err := url.Parse(rawSasURL)
	a.AssertNoErr(err)
	r.rawSasURL = rawURL
	if s.GetModifiableParameters().relativeSourcePath != "" {
		r.appendSourcePath(s.GetModifiableParameters().relativeSourcePath, true)
	}
}

func (r *resourceBlobContainer) createFiles(a asserter, s *scenario, isSource bool) {
	options := &generateBlobFromListOptions{
		rawSASURL:       *r.rawSasURL,
		containerClient: r.containerClient,
		generateFromListOptions: generateFromListOptions{
			fs:          s.fs.allObjects(isSource),
			defaultSize: s.fs.defaultSize,
			accountType: r.accountType,
		},
	}
	if s.fromTo.IsDownload() {
		options.cpkInfo = common.GetCpkInfo(s.p.cpkByValue)
		options.cpkScopeInfo = common.GetCpkScopeInfo(s.p.cpkByName)
	}
	if isSource {
		options.accessTier = s.p.accessTier
	}
	scenarioHelper{}.generateBlobsFromList(a, options)

	// set root ACL
	if r.accountType == EAccountType.HierarchicalNamespaceEnabled() {
		containerURLParts, err := blob.ParseURL(r.containerClient.URL())
		a.AssertNoErr(err)

		for _, v := range options.generateFromListOptions.fs {
			if v.name == "" {
				if v.creationProperties.adlsPermissionsACL == nil {
					break
				}

				rootURL := TestResourceFactory{}.GetDatalakeServiceURL(r.accountType).NewFileSystemURL(containerURLParts.ContainerName).NewDirectoryURL("/")

				_, err := rootURL.SetAccessControl(ctx, azbfs.BlobFSAccessControl{
					ACL: *v.creationProperties.adlsPermissionsACL,
				})
				a.AssertNoErr(err)

				break
			}
		}
	}
}

func (r *resourceBlobContainer) createFile(a asserter, o *testObject, s *scenario, isSource bool) {
	options := &generateBlobFromListOptions{
		containerClient: r.containerClient,
		generateFromListOptions: generateFromListOptions{
			fs:          []*testObject{o},
			defaultSize: s.fs.defaultSize,
		},
	}

	if s.fromTo.IsDownload() || s.fromTo.IsDelete() {
		options.cpkInfo = common.GetCpkInfo(s.p.cpkByValue)
		options.cpkScopeInfo = common.GetCpkScopeInfo(s.p.cpkByName)
	}

	scenarioHelper{}.generateBlobsFromList(a, options)
}

func (r *resourceBlobContainer) cleanup(a asserter) {
	if r.containerClient != nil {
		deleteContainer(a, r.containerClient)
	}
}

func (r *resourceBlobContainer) getParam(stripTopDir bool, withSas bool, withFile string) string {
	var uri string
	if withSas {
		uri = r.rawSasURL.String()
	} else {
		uri = r.containerClient.URL()
	}

	if withFile != "" {
		bURLParts, _ := blob.ParseURL(uri)

		bURLParts.BlobName = withFile

		uri = bURLParts.String()
	}

	if r.accountType == EAccountType.HierarchicalNamespaceEnabled() {
		uri = strings.ReplaceAll(uri, "blob", "dfs")
	}

	return uri
}

func (r *resourceBlobContainer) getSAS() string {
	return "?" + r.rawSasURL.RawQuery
}

func (r *resourceBlobContainer) isContainerLike() bool {
	return true
}

func (r *resourceBlobContainer) appendSourcePath(filePath string, useSas bool) {
	if useSas {
		r.rawSasURL.Path += "/" + filePath
	}
}

func (r *resourceBlobContainer) getAllProperties(a asserter) map[string]*objectProperties {
	var fileSystem *azbfs.FileSystemURL
	if r.accountType == EAccountType.HierarchicalNamespaceEnabled() {
		urlParts, err := blob.ParseURL(r.containerClient.URL())
		a.AssertNoErr(err)
		fsURL := TestResourceFactory{}.GetDatalakeServiceURL(r.accountType).NewFileSystemURL(urlParts.ContainerName)
		fileSystem = &fsURL
	}

	objects := scenarioHelper{}.enumerateContainerBlobProperties(a, r.containerClient, fileSystem)

	if fileSystem != nil {
		fsURL := fileSystem.NewDirectoryURL("/")

		ACL, err := fsURL.GetAccessControl(ctx)
		if stgErr, ok := err.(azbfs.StorageError); ok {
			if stgErr.ServiceCode() == "FilesystemNotFound" { // skip grabbing ACLs
				return objects
			}
		}
		a.AssertNoErr(err)

		objects[""] = &objectProperties{
			entityType:         common.EEntityType.Folder(),
			adlsPermissionsACL: &ACL.ACL,
		}
	}

	return objects
}

func (r *resourceBlobContainer) downloadContent(a asserter, options downloadContentOptions) []byte {
	options.containerClient = r.containerClient
	return scenarioHelper{}.downloadBlobContent(a, options)
}

func (r *resourceBlobContainer) createSourceSnapshot(a asserter) {
	panic("Not Implemented")
}

// ///

type resourceAzureFileShare struct {
	accountType AccountType
	shareClient *share.Client // // TODO: Either eliminate SDK URLs from ResourceManager or provide means to edit it (File SDK) for which pipeline is required
	rawSasURL   *url.URL
	snapshotID  string // optional, use a snapshot as the location instead
}

func (r *resourceAzureFileShare) createLocation(a asserter, s *scenario) {
	su, _, rawSasURL := TestResourceFactory{}.CreateNewFileShare(a, EAccountType.Standard())
	r.shareClient = su
	rawURL, err := url.Parse(rawSasURL)
	a.AssertNoErr(err)
	r.rawSasURL = rawURL
	if s.GetModifiableParameters().relativeSourcePath != "" {
		r.appendSourcePath(s.GetModifiableParameters().relativeSourcePath, true)
	}
}

func (r *resourceAzureFileShare) createFiles(a asserter, s *scenario, isSource bool) {
	scenarioHelper{}.generateAzureFilesFromList(a, &generateAzureFilesFromListOptions{
		shareClient: r.shareClient,
		fileList:    s.fs.allObjects(isSource),
		defaultSize: s.fs.defaultSize,
	})
}

func (r *resourceAzureFileShare) createFile(a asserter, o *testObject, s *scenario, isSource bool) {
	scenarioHelper{}.generateAzureFilesFromList(a, &generateAzureFilesFromListOptions{
		shareClient: r.shareClient,
		fileList:    []*testObject{o},
		defaultSize: s.fs.defaultSize,
	})
}

func (r *resourceAzureFileShare) cleanup(a asserter) {
	if r.shareClient != nil {
		deleteShare(a, r.shareClient)
	}
}

func (r *resourceAzureFileShare) getParam(stripTopDir bool, withSas bool, withFile string) string {
	assertNoStripTopDir(stripTopDir)
	var uri string
	if withSas {
		uri = r.rawSasURL.String()
	} else {
		uri = r.shareClient.URL()
	}

	// append the snapshot ID if present
	if r.snapshotID != "" || withFile != "" {
		parts, _ := file.ParseURL(uri)
		if r.snapshotID != "" {
			parts.ShareSnapshot = r.snapshotID
		}

		if withFile != "" {
			parts.DirectoryOrFilePath = withFile
		}
		uri = parts.String()
	}

	return uri
}

func (r *resourceAzureFileShare) getSAS() string {
	return "?" + r.rawSasURL.RawQuery
}

func (r *resourceAzureFileShare) isContainerLike() bool {
	return true
}

func (r *resourceAzureFileShare) appendSourcePath(filePath string, useSas bool) {
	if useSas {
		r.rawSasURL.Path += "/" + filePath
	}
}

func (r *resourceAzureFileShare) getAllProperties(a asserter) map[string]*objectProperties {
	return scenarioHelper{}.enumerateShareFileProperties(a, r.shareClient)
}

func (r *resourceAzureFileShare) downloadContent(a asserter, options downloadContentOptions) []byte {
	return scenarioHelper{}.downloadFileContent(a, downloadContentOptions{
		resourceRelPath: options.resourceRelPath,
		downloadFileContentOptions: downloadFileContentOptions{
			shareClient: r.shareClient,
		},
	})
}

func (r *resourceAzureFileShare) createSourceSnapshot(a asserter) {
	r.snapshotID = TestResourceFactory{}.CreateNewFileShareSnapshot(a, r.shareClient)
}

// //

type resourceManagedDisk struct {
	config    ManagedDiskConfig
	accessURI *url.URL
}

// Typically, createLocation would well, create the location.
// However, resourceManagedDisk hijacks that for calling getAccess
func (r *resourceManagedDisk) createLocation(a asserter, s *scenario) {
	uri, err := r.config.GetAccess()
	a.AssertNoErr(err)

	r.accessURI = uri
}

func (r *resourceManagedDisk) createFiles(a asserter, s *scenario, isSource bool) {
	// No-op.
}

func (r *resourceManagedDisk) createFile(a asserter, o *testObject, s *scenario, isSource bool) {
	// No-op.
}

func (r *resourceManagedDisk) getAllProperties(a asserter) map[string]*objectProperties {
	// No-op.
	return map[string]*objectProperties{}
}

func (r *resourceManagedDisk) downloadContent(a asserter, options downloadContentOptions) []byte {
	panic("md testing currently does not involve custom content; just a zeroed out disk")
}

// cleanup also usurps traditional resourceManager functionality.
func (r *resourceManagedDisk) cleanup(a asserter) {
	// revoking access isn't required and causes funky behaviour for testing that might require a distributed mutex.
	// todo: we should create managed disks as needed with the requirements rather than using a single MD should we plan to do read-write tests.
}

// getParam works functionally different because resourceManagerDisk inherently only targets a single file.
func (r *resourceManagedDisk) getParam(stripTopDir bool, withSas bool, withFile string) string {
	out := *r.accessURI // clone the URI

	if !withSas {
		out.RawQuery = ""
	}

	return out.String()
}

func (r *resourceManagedDisk) getSAS() string {
	// TODO implement me
	panic("implement me")
}

func (r *resourceManagedDisk) isContainerLike() bool {
	return false
}

func (r *resourceManagedDisk) appendSourcePath(s string, b bool) {
	panic("resourceManagedDisk is a single file")
}

func (r *resourceManagedDisk) createSourceSnapshot(a asserter) {
	// TODO implement me
	panic("cannot snapshot a managed disk")
}

// //

type resourceDummy struct{}

func (r *resourceDummy) createLocation(a asserter, s *scenario) {

}

func (r *resourceDummy) createFiles(a asserter, s *scenario, isSource bool) {

}

func (r *resourceDummy) createFile(a asserter, o *testObject, s *scenario, isSource bool) {

}

func (r *resourceDummy) cleanup(_ asserter) {
}

func (r *resourceDummy) getParam(stripTopDir bool, withSas bool, withFile string) string {
	assertNoStripTopDir(stripTopDir)
	return ""
}

func (r *resourceDummy) getSAS() string {
	return ""
}

func (r *resourceDummy) isContainerLike() bool {
	return false
}

func (r *resourceDummy) getAllProperties(a asserter) map[string]*objectProperties {
	panic("not impelmented")
}

func (r *resourceDummy) downloadContent(a asserter, options downloadContentOptions) []byte {
	return make([]byte, 0)
}

func (r *resourceDummy) appendSourcePath(_ string, _ bool) {
}

func (r *resourceDummy) createSourceSnapshot(a asserter) {}
